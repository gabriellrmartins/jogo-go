package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	BoardWidth    = 20
	BoardHeight   = 15
	NumItems      = 15
	GameTickDelay = 150 * time.Millisecond
)

// --- Estruturas de Dados do Jogo ---
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type Player struct {
	ID       string          `json:"id"`
	Pos      Point           `json:"pos"`
	Score    int             `json:"score"`
	conn     *websocket.Conn `json:"-"` // Não serializar para estado completo/delta
	sendChan chan []byte     `json:"-"` // Não serializar
	IsActive bool            // Usado internamente, mas o cliente deduz pela presença/ausência
}

type Item struct {
	ID  string `json:"id"`
	Pos Point  `json:"pos"`
}

// --- Estruturas de Mensagem Servidor -> Cliente ---
const (
	MsgTypeWelcome     = "welcome"
	MsgTypeFullState   = "full_state"
	MsgTypeDeltaUpdate = "delta_update"
)

type ServerMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

type WelcomePayload struct {
	PlayerID string `json:"playerId"`
}

// GameStateForClient é uma representação do GameState para enviar aos clientes (sem campos internos)
type GameStateForClient struct {
	Players     map[string]*Player `json:"players"` // Enviará apenas os campos serializáveis de Player
	Items       map[string]*Item   `json:"items"`
	BoardWidth  int                `json:"boardWidth"`
	BoardHeight int                `json:"boardHeight"`
	GameOver    bool               `json:"gameOver"`
	WinnerID    string             `json:"winnerId,omitempty"`
}

type PlayerDelta struct {
	ID    string `json:"id"`
	Pos   *Point `json:"pos,omitempty"`
	Score *int   `json:"score,omitempty"`
}

type GameStatusDelta struct {
	GameOver bool   `json:"gameOver"`
	WinnerID string `json:"winnerId,omitempty"`
}

type DeltaPayload struct {
	PlayersUpdated map[string]PlayerDelta `json:"playersUpdated,omitempty"`
	PlayersRemoved []string               `json:"playersRemoved,omitempty"`
	ItemsAdded     []Item                 `json:"itemsAdded,omitempty"`   // Lista de novos itens
	ItemsRemoved   []string               `json:"itemsRemoved,omitempty"` // Chaves "x,y" dos itens
	GameStatus     *GameStatusDelta       `json:"gameStatus,omitempty"`
}

// GameState agora com pendingDeltas
type GameState struct {
	Players     map[string]*Player
	Items       map[string]*Item
	BoardWidth  int
	BoardHeight int
	GameOver    bool
	WinnerID    string

	pendingDeltas DeltaPayload // Acumulador de mudanças
	mu            sync.Mutex
}

// ClientMessage permanece o mesmo
type ClientMessage struct {
	Action    string `json:"action"`
	Direction string `json:"direction"`
}

var game = &GameState{ // Inicialização sem os campos que precisam de make
	BoardWidth:  BoardWidth,
	BoardHeight: BoardHeight,
}

func (gs *GameState) resetPendingDeltas() {
	gs.pendingDeltas = DeltaPayload{
		PlayersUpdated: make(map[string]PlayerDelta),
		PlayersRemoved: []string{}, // Iniciar slices vazios
		ItemsAdded:     []Item{},
		ItemsRemoved:   []string{},
		GameStatus:     nil, // Nenhuma mudança de status por padrão
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (gs *GameState) initializeItems() {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	gs.resetPendingDeltas() // Começa um novo conjunto de deltas

	currentItems := make(map[string]*Item) // Mapa temporário para novos itens
	newItemsListForDelta := []Item{}

	for i := 0; i < NumItems; i++ {
		var itemPos Point
		uniquePos := false
		for !uniquePos {
			itemPos = Point{X: rand.Intn(BoardWidth), Y: rand.Intn(BoardHeight)}
			key := fmt.Sprintf("%d,%d", itemPos.X, itemPos.Y)
			_, currentExists := currentItems[key]
			playerOccupies := false
			for _, p := range gs.Players {
				if p.Pos.X == itemPos.X && p.Pos.Y == itemPos.Y {
					playerOccupies = true
					break
				}
			}
			if !currentExists && !playerOccupies {
				uniquePos = true
			}
		}
		itemID := "item_" + strconv.Itoa(i)
		itemKey := fmt.Sprintf("%d,%d", itemPos.X, itemPos.Y)
		newItem := Item{ID: itemID, Pos: itemPos}
		currentItems[itemKey] = &newItem
		newItemsListForDelta = append(newItemsListForDelta, newItem)
	}
	gs.Items = currentItems
	gs.pendingDeltas.ItemsAdded = newItemsListForDelta

	gs.GameOver = false
	gs.WinnerID = ""
	// Garante que GameStatus seja inicializado se for nil no delta
	if gs.pendingDeltas.GameStatus == nil && (gs.GameOver || gs.WinnerID != "") { // Condição para criar
		gs.pendingDeltas.GameStatus = &GameStatusDelta{}
	}
	if gs.pendingDeltas.GameStatus != nil { // Atualiza se já existe
		gs.pendingDeltas.GameStatus.GameOver = false
		gs.pendingDeltas.GameStatus.WinnerID = ""
	} else { // Cria se não existe e precisa ser criado
		gs.pendingDeltas.GameStatus = &GameStatusDelta{GameOver: false, WinnerID: ""}
	}

	for playerID, player := range gs.Players {
		if player.IsActive {
			player.Score = 0
			delta, ok := gs.pendingDeltas.PlayersUpdated[playerID]
			if !ok {
				delta = PlayerDelta{ID: playerID}
			}
			score := 0
			delta.Score = &score
			gs.pendingDeltas.PlayersUpdated[playerID] = delta
		}
	}
	log.Printf("Jogo resetado. %d itens. Pontuações zeradas. Deltas preparados.", len(gs.Items))
}

func (gs *GameState) addPlayer(id string, conn *websocket.Conn) *Player {
	var startPos Point
	uniquePos := false

	gs.mu.Lock() // Bloqueia para encontrar posição e modificar estado
	defer gs.mu.Unlock()

	for !uniquePos {
		startPos = Point{X: rand.Intn(BoardWidth), Y: rand.Intn(BoardHeight)}
		occupied := false
		for _, p := range gs.Players { // Verifica jogadores existentes
			if p.Pos.X == startPos.X && p.Pos.Y == startPos.Y {
				occupied = true
				break
			}
		}
		if occupied {
			continue
		}
		itemKey := fmt.Sprintf("%d,%d", startPos.X, startPos.Y)
		if _, exists := gs.Items[itemKey]; exists {
			occupied = true
		} // Verifica itens existentes
		if !occupied {
			uniquePos = true
		}
	}

	player := &Player{
		ID:       id,
		Pos:      startPos,
		Score:    0,
		conn:     conn,
		sendChan: make(chan []byte, 256),
		IsActive: true,
	}
	gs.Players[id] = player

	// Adiciona este novo jogador ao delta para notificar OUTROS clientes
	// (o novo jogador receberá o estado completo)
	score := 0 // Score inicial é 0
	pos := player.Pos
	gs.pendingDeltas.PlayersUpdated[id] = PlayerDelta{
		ID:    id,
		Pos:   &pos,   // Envia a posição inicial
		Score: &score, // Envia o score inicial
	}
	log.Printf("Jogador %s (%s) entrou. Delta de entrada preparado.", player.ID, id)
	return player
}

func (gs *GameState) removePlayer(id string) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	if player, ok := gs.Players[id]; ok {
		player.IsActive = false
		close(player.sendChan)
		delete(gs.Players, id)
		gs.pendingDeltas.PlayersRemoved = append(gs.pendingDeltas.PlayersRemoved, id)

		// Remove o jogador de PlayersUpdated se ele acabou de entrar e saiu antes de um broadcast
		delete(gs.pendingDeltas.PlayersUpdated, id)
		log.Printf("Jogador %s removido. Delta de remoção preparado.", id)
	}
}

func (gs *GameState) handlePlayerMove(playerID string, direction string) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	if gs.GameOver {
		return
	}
	player, ok := gs.Players[playerID]
	if !ok || !player.IsActive {
		return
	}

	oldPos := player.Pos
	newPos := player.Pos
	switch direction {
	case "up":
		if newPos.Y > 0 {
			newPos.Y--
		}
	case "down":
		if newPos.Y < BoardHeight-1 {
			newPos.Y++
		}
	case "left":
		if newPos.X > 0 {
			newPos.X--
		}
	case "right":
		if newPos.X < BoardWidth-1 {
			newPos.X++
		}
	default:
		return
	}

	playerMoved := (oldPos != newPos)
	if playerMoved {
		player.Pos = newPos
		delta, ok := gs.pendingDeltas.PlayersUpdated[playerID]
		if !ok {
			delta = PlayerDelta{ID: playerID}
		}
		posCopy := newPos // Copia para ponteiro estável
		delta.Pos = &posCopy
		gs.pendingDeltas.PlayersUpdated[playerID] = delta
	}

	itemKey := fmt.Sprintf("%d,%d", newPos.X, newPos.Y)
	if _, exists := gs.Items[itemKey]; exists {
		player.Score++
		delete(gs.Items, itemKey)

		delta, ok := gs.pendingDeltas.PlayersUpdated[playerID]
		if !ok {
			delta = PlayerDelta{ID: playerID}
		}
		scoreCopy := player.Score
		delta.Score = &scoreCopy
		gs.pendingDeltas.PlayersUpdated[playerID] = delta

		gs.pendingDeltas.ItemsRemoved = append(gs.pendingDeltas.ItemsRemoved, itemKey)
		// log.Printf("Jogador %s coletou item. Deltas preparados.", player.ID) // Log pode ser muito verboso aqui

		if len(gs.Items) == 0 {
			gs.GameOver = true
			winnerScore := -1
			var winners []string
			for _, p := range gs.Players {
				if p.IsActive {
					if p.Score > winnerScore {
						winnerScore = p.Score
						winners = []string{p.ID}
					} else if p.Score == winnerScore {
						winners = append(winners, p.ID)
					}
				}
			}
			if len(winners) > 0 {
				gs.WinnerID = fmt.Sprintf("%v", winners)
			}

			// Garante que GameStatus seja inicializado se for nil
			if gs.pendingDeltas.GameStatus == nil {
				gs.pendingDeltas.GameStatus = &GameStatusDelta{}
			}
			gs.pendingDeltas.GameStatus.GameOver = true
			gs.pendingDeltas.GameStatus.WinnerID = gs.WinnerID
		}
	}
}

// writer é uma goroutine que envia mensagens do `sendChan` para o WebSocket do jogador
func writer(player *Player) {
	defer func() {
		player.conn.Close()
		// log.Printf("Escritor para o jogador %s encerrado.", player.ID) // Log pode ser verboso
	}()

	for message := range player.sendChan {
		if err := player.conn.WriteMessage(websocket.TextMessage, message); err != nil {
			// log.Printf("Erro ao escrever para jogador %s: %v", player.ID, err) // Log pode ser verboso
			return
		}
	}
}

// reader é uma goroutine que lê mensagens do WebSocket do jogador
func reader(player *Player) {
	defer func() {
		log.Printf("Leitor para o jogador %s encerrando. Realizando limpeza.", player.ID)
		game.removePlayer(player.ID)
	}()

	player.conn.SetReadLimit(512)
	for {
		messageType, p, err := player.conn.ReadMessage()
		if err != nil {
			// if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
			// 	log.Printf("Erro de conexão inesperado para jogador %s: %v", player.ID, err)
			// } else {
			// 	log.Printf("Jogador %s desconectado: %v", player.ID, err)
			// } // Logs podem ser verbosos
			break
		}

		if messageType == websocket.TextMessage {
			var msg ClientMessage
			if err := json.Unmarshal(p, &msg); err != nil {
				log.Printf("Erro ao deserializar mensagem de %s: %v", player.ID, err)
				continue
			}

			if msg.Action == "move" {
				game.handlePlayerMove(player.ID, msg.Direction)
			} else if msg.Action == "reset_game_request" && game.GameOver {
				log.Printf("Jogador %s solicitou reset do jogo.", player.ID)
				game.initializeItems() // Isso preparará os deltas para o reset
			}
		}
	}
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Falha no upgrade: %v", err)
		return
	}

	playerID := uuid.NewString()
	// addPlayer agora é protegido por mutex e prepara o delta para outros jogadores
	player := game.addPlayer(playerID, conn)

	go writer(player)
	go reader(player)

	welcomeMsg := ServerMessage{Type: MsgTypeWelcome, Payload: WelcomePayload{PlayerID: player.ID}}
	welcomeData, _ := json.Marshal(welcomeMsg)
	select {
	case player.sendChan <- welcomeData:
	default:
		log.Printf("Canal de boas-vindas cheio para %s", player.ID)
	}

	game.mu.Lock()
	fullStatePayload := GameStateForClient{
		Players:     make(map[string]*Player),
		Items:       make(map[string]*Item), // Copia o mapa de itens
		BoardWidth:  game.BoardWidth,
		BoardHeight: game.BoardHeight,
		GameOver:    game.GameOver,
		WinnerID:    game.WinnerID,
	}
	for id, p := range game.Players { // Copia jogadores ativos para o DTO
		if p.IsActive {
			playerCopy := *p
			playerCopy.conn = nil
			playerCopy.sendChan = nil
			fullStatePayload.Players[id] = &playerCopy
		}
	}
	for key, item := range game.Items { // Copia itens
		itemCopy := *item
		fullStatePayload.Items[key] = &itemCopy
	}
	game.mu.Unlock()

	fullStateMsg := ServerMessage{Type: MsgTypeFullState, Payload: fullStatePayload}
	fullStateData, err := json.Marshal(fullStateMsg)
	if err != nil {
		log.Printf("Erro ao serializar estado completo para %s: %v", player.ID, err)
		return
	}
	select {
	case player.sendChan <- fullStateData:
	default:
		log.Printf("Canal de estado completo cheio para %s", player.ID)
	}
}

func broadcastUpdates() {
	game.mu.Lock()
	if len(game.pendingDeltas.PlayersUpdated) == 0 &&
		len(game.pendingDeltas.PlayersRemoved) == 0 &&
		len(game.pendingDeltas.ItemsAdded) == 0 &&
		len(game.pendingDeltas.ItemsRemoved) == 0 &&
		game.pendingDeltas.GameStatus == nil {
		game.mu.Unlock()
		return
	}

	deltasToSend := game.pendingDeltas // Copia os deltas
	game.resetPendingDeltas()          // Reseta o acumulador para o próximo ciclo
	game.mu.Unlock()                   // Libera o lock antes de enviar

	deltaMsg := ServerMessage{Type: MsgTypeDeltaUpdate, Payload: deltasToSend}
	messageData, err := json.Marshal(deltaMsg)
	if err != nil {
		log.Printf("Erro ao serializar deltas: %v", err)
		return
	}

	var activePlayerChans []chan []byte
	game.mu.Lock() // Lock para pegar a lista de canais de jogadores ativos
	for _, p := range game.Players {
		if p.IsActive {
			activePlayerChans = append(activePlayerChans, p.sendChan)
		}
	}
	game.mu.Unlock()

	// Log dos deltas apenas se houver algo significativo (para não poluir com deltas vazios se a lógica permitir)
	// if len(deltasToSend.PlayersUpdated) > 0 || len(deltasToSend.PlayersRemoved) > 0 || len(deltasToSend.ItemsAdded) > 0 || len(deltasToSend.ItemsRemoved) > 0 || deltasToSend.GameStatus != nil {
	// 	log.Printf("Enviando deltas: PlayersUpdated: %d, PlayersRemoved: %d, ItemsAdded: %d, ItemsRemoved: %d, GameStatus: %v",
	// 		len(deltasToSend.PlayersUpdated), len(deltasToSend.PlayersRemoved), len(deltasToSend.ItemsAdded), len(deltasToSend.ItemsRemoved), deltasToSend.GameStatus != nil)
	// }

	for _, ch := range activePlayerChans {
		select {
		case ch <- messageData:
		default:
			// log.Println("Um canal de jogador estava cheio ao enviar deltas.") // Log pode ser verboso
		}
	}
}

func gameLoop() {
	game.mu.Lock()
	game.Players = make(map[string]*Player) // Inicializa o mapa de jogadores
	game.Items = make(map[string]*Item)     // Inicializa o mapa de itens
	game.resetPendingDeltas()               // Inicializa os deltas pendentes
	game.mu.Unlock()

	game.initializeItems() // Popula os itens iniciais e prepara o primeiro delta (para um possível broadcast se houvesse jogadores)

	ticker := time.NewTicker(GameTickDelay)
	defer ticker.Stop()
	for {
		<-ticker.C
		broadcastUpdates()
	}
}

func main() {
	rand.Seed(time.Now().UnixNano())

	http.HandleFunc("/ws", wsHandler)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		html := `
<!DOCTYPE html>
<html lang="pt-BR">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Go Diamond Collector</title>
    <style>
        :root {
            --primary-bg: #f4f7f6; /* Fundo principal da página */
            --secondary-bg: #ffffff; /* Fundo de containers como info, board-wrapper */
            --accent-color: #3498db; /* Azul suave para títulos, botões */
            --accent-hover: #2980b9; /* Hover do accent-color */
            --text-color: #333333; /* Cor principal do texto */
            --border-color: #dddddd; /* Cor para bordas sutis */
            --item-bg: #f1c40f; /* Dourado para itens */
            --player-bg: #87ceeb; /* Azul céu para outros jogadores */
            --self-player-bg: #5dade2; /* Azul mais forte para o jogador local */
            --shadow-color: rgba(0,0,0,0.08); /* Sombra mais suave */
        }
        body { 
            font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; 
            display: flex; 
            flex-direction: column; 
            align-items: center; 
            margin: 0; 
            padding: 20px; 
            background-color: var(--primary-bg); 
            color: var(--text-color);
            line-height: 1.6;
        }
        h1 { 
            margin-bottom: 0.5em;
            font-size: 2.2em; 
            color: var(--accent-color);
            font-weight: 300; /* Fonte mais leve para o título */
            text-align: center;
        }
        #game-description {
            background-color: var(--secondary-bg);
            padding: 20px 25px; /* Mais padding */
            border-radius: 8px;
            margin-bottom: 30px; /* Mais espaço */
            max-width: 700px;
            width: 90%;
            box-shadow: 0 4px 8px var(--shadow-color); /* Sombra mais pronunciada */
            text-align: left;
        }
        #game-description h2 {
            margin-top: 0;
            color: var(--accent-color);
            font-size: 1.4em;
            font-weight: 500; /* Peso médio */
            border-bottom: 1px solid var(--border-color);
            padding-bottom: 0.5em;
            margin-bottom: 1em; /* Mais espaço */
        }
        #game-description p, #game-description ul {
            font-size: 0.95em;
            margin-bottom: 1em;
        }
        #game-description ul {
            list-style-position: inside; /* Bolinhas dentro do padding */
            padding-left: 0; /* Remove padding padrão do ul */
        }
         #game-description li {
            margin-bottom: 0.5em;
        }
        #game-description strong {
            font-weight: 600; /* Destaca um pouco mais */
            /* color: var(--accent-color); // Mantém a cor do texto para não sobrecarregar */
        }
        #game-container { 
            display: flex; 
            flex-wrap: wrap; 
            gap: 25px; 
            justify-content: center;
            width: 100%;
            align-items: flex-start; /* Alinha os itens no topo */
        }
        #board-wrapper { 
            width: auto; 
            max-width: 100%; 
            overflow-x: auto; 
            display: flex;
            justify-content: center; 
            padding: 10px; /* Padding interno */
            background-color: var(--secondary-bg);
            border-radius: 8px;
            box-shadow: 0 4px 8px var(--shadow-color);
        }
        #board {
            border-collapse: collapse;
            font-family: monospace;
            table-layout: fixed; 
            border: 1px solid var(--border-color); 
        }
        #board td {
            border: 1px solid #e7e7e7; 
            width: 30px;   
            height: 30px;  
            text-align: center;
            vertical-align: middle;
            font-size: 16px; 
            overflow: hidden; 
            box-sizing: border-box; 
            white-space: nowrap; 
            line-height: 28px; 
        }
        .player { background-color: var(--player-bg); border-radius: 50%; }
        .item { background-color: var(--item-bg); color: white; border-radius: 3px; animation: pulseItem 1.5s infinite ease-in-out; }
        .self { font-weight: bold; background-color: var(--self-player-bg); box-shadow: 0 0 5px 3px var(--accent-hover); } 
        @keyframes pulseItem {
            0% { transform: scale(0.9); opacity: 0.8; }
            50% { transform: scale(1.05); opacity: 1; }
            100% { transform: scale(0.9); opacity: 0.8; }
        }
        #info { 
            text-align: left; 
            padding: 20px; 
            border: 1px solid var(--border-color); 
            background-color: var(--secondary-bg); 
            border-radius: 8px; 
            min-width: 280px; 
            width: auto; /* Para não forçar largura em flex */
            max-width: 350px; /* Limita a largura máxima */
            box-shadow: 0 4px 8px var(--shadow-color);
        }
        #info h3 { 
            margin-top: 0; 
            margin-bottom: 10px; 
            font-size: 1.3em;
            color: var(--accent-color);
            font-weight: 500;
        }
        #info pre { 
            margin-top: 5px; 
            margin-bottom: 15px; 
            white-space: pre-wrap; 
            background-color: #f9f9f9; 
            padding: 10px;
            border-radius: 4px;
            font-size: 0.9em;
            border: 1px solid #efefef;
        }
        #controls { 
            margin-top: 25px; 
            text-align: center; 
            width: 100%; 
        }
        #controls button { 
            padding: 12px 20px; 
            margin: 8px; 
            font-size: 1.05em; 
            cursor: pointer; 
            border: none; 
            border-radius: 5px;
            background-color: var(--accent-color); 
            color: white;
            transition: background-color 0.2s ease, transform 0.1s ease;
            min-width: 80px; 
        }
        #controls button:hover { background-color: var(--accent-hover); }
        #controls button:active { transform: scale(0.95); }

        #log-container { width: 100%; max-width: 700px; margin-top:25px; }
        #log { 
            font-size:0.85em; 
            max-height: 120px; 
            overflow-y: scroll; 
            border: 1px solid var(--border-color); 
            padding:10px; 
            background-color: var(--secondary-bg);
            white-space: pre-wrap; 
            word-break: break-all;
            border-radius: 4px;
            font-family: monospace;
        }
        #game-over-msg { 
            padding: 15px;
            background-color: #ffebee; /* Vermelho claro para game over */
            border: 1px solid #ffcdd2;
            color: #c62828; /* Vermelho escuro */
            font-weight:bold; 
            margin-bottom: 15px; 
            font-size: 1.2em; 
            border-radius: 5px;
            text-align: center;
            display: none; 
        }
        #resetButton {
            background-color: #5bc0de; 
        }
        #resetButton:hover {
            background-color: #31b0d5;
        }

        /* === Media Queries para Responsividade === */
        @media (max-width: 768px) {
            body { padding: 15px; }
            h1 { font-size: 1.8em; }
            #game-description { width: 90%; padding: 15px; margin-bottom: 20px;}
            #game-description h2 { font-size: 1.3em; }
            #game-description p, #game-description ul { font-size: 0.9em; }

            #game-container {
                flex-direction: column; 
                align-items: center;
                gap: 20px;
            }
            #board-wrapper { margin-bottom: 20px; }
            #board td {
                width: 26px;  
                height: 26px;
                font-size: 14px; 
                line-height: 24px;
            }
            #info {
                width: 90%; 
                max-width: 100%; /* Permite que o info ocupe mais espaço se necessário */
                min-width: unset;
                padding: 15px;
            }
             #info h3 { font-size: 1.2em; }

            #controls {
                display: grid;
                grid-template-columns: 1fr 1fr 1fr;
                grid-template-rows: auto auto auto;
                gap: 10px; 
                max-width: 250px; 
                margin-left: auto;
                margin-right: auto;
                padding: 15px;
                background-color: var(--secondary-bg);
                border-radius: 10px;
                box-shadow: 0 2px 4px var(--shadow-color);
            }
            #controls button {
                margin: 0; 
                width: 100%; 
                height: 55px; 
                font-size: 1em;
                display: flex; 
                align-items: center;
                justify-content: center;
            }
            #btn-up    { grid-column: 2; grid-row: 1; }
            #btn-left  { grid-column: 1; grid-row: 2; }
            #btn-placeholder { grid-column: 2; grid-row: 2; visibility: hidden; } 
            #btn-right { grid-column: 3; grid-row: 2; }
            #btn-down  { grid-column: 2; grid-row: 3; }

            #controls br { display: none; } 
        }

        @media (max-width: 480px) {
            h1 { font-size: 1.6em; }
            #game-description h2 { font-size: 1.2em; }
            #board td {
                width: 22px;  
                height: 22px;
                font-size: 12px;
                line-height: 20px;
            }
            #controls {
                max-width: 220px; 
                gap: 8px;
                padding: 10px;
            }
            #controls button {
                height: 50px;
                font-size: 0.95em;
            }
            #info { width: 95%; padding: 12px; }
             #info h3 { font-size: 1.1em; }
             #info pre { font-size: 0.85em; padding: 8px;}
        }
    </style>
</head>
<body>
    <h1>Go Diamond Collector</h1>

    <div id="game-description">
        <h2>Como Jogar:</h2>
        <p><strong>Objetivo:</strong> Ser o jogador com mais diamantes (💎) coletados quando todos os itens do tabuleiro acabarem!</p>
        <ul>
            <li>Use as teclas <strong>W, A, S, D</strong> ou as <strong>Setas Direcionais</strong> do teclado para se mover.</li>
            <li>Em dispositivos móveis, use os <strong>botões de controle</strong> na tela.</li>
            <li>Passe por cima de um diamante (💎) para coletá-lo e aumentar sua pontuação.</li>
            <li>Fique de olho na pontuação dos outros jogadores!</li>
            <li>O jogo termina quando não houver mais diamantes. O jogador com mais diamantes vence. Boa sorte!</li>
        </ul>
    </div>

    <div id="game-container">
        <div id="board-wrapper"> 
            <table id="board"></table>
        </div>
        <div id="info">
            <h3>Seu ID: <span id="my-id">---</span></h3>
            <h3>Pontuações:</h3>
            <pre id="scores"></pre>
            <div id="game-over-msg"></div>
            <button id="resetButton" style="display:none;">Resetar Jogo</button>
        </div>
    </div>
    <div id="controls">
        <button id="btn-up" onclick="sendMove('up')" title="Mover para Cima (W ou Seta para Cima)">&#x25B2;</button> 
        <br> 
        <button id="btn-left" onclick="sendMove('left')" title="Mover para Esquerda (A ou Seta para Esquerda)">&#x25C0;</button> 
        <span id="btn-placeholder"></span> 
        <button id="btn-right" onclick="sendMove('right')" title="Mover para Direita (D ou Seta para Direita)">&#x25B6;</button> 
        <br> 
        <button id="btn-down" onclick="sendMove('down')" title="Mover para Baixo (S ou Seta para Baixo)">&#x25BC;</button> 
    </div>
    <div id="log-container">
      <h4>Log de Eventos (Debug):</h4>
      <pre id="log"></pre>
    </div>

    <script>
        const boardElement = document.getElementById('board');
        const scoresElement = document.getElementById('scores');
        const logElement = document.getElementById('log'); 
        const myIdElement = document.getElementById('my-id');
        const gameOverMsgElement = document.getElementById('game-over-msg');
        const resetButton = document.getElementById('resetButton');

        const wsProtocol = window.location.protocol === "https:" ? "wss:" : "ws:";
        const ws = new WebSocket(wsProtocol + "//" + window.location.host + "/ws");
        let myPlayerId = null;

        let localGameState = {
            players: {},
            items: {},
            boardWidth: ${BoardWidth}, 
            boardHeight: ${BoardHeight},
            gameOver: false,
            winnerId: null
        };

        function clientLog(message) {
            console.log(message); 
            const now = new Date();
            const timeString = now.getHours().toString().padStart(2, '0') + ':' + 
                               now.getMinutes().toString().padStart(2, '0') + ':' + 
                               now.getSeconds().toString().padStart(2, '0');
            if (logElement.textContent.length > 2000) { 
                logElement.textContent = logElement.textContent.substring(0,1500);
            }
            logElement.textContent = timeString + ": " + message + "\n" + logElement.textContent;
        }

        function drawBoard(gameStateToDraw) { 
            boardElement.innerHTML = ''; 
            for (let y = 0; y < gameStateToDraw.boardHeight; y++) {
                const row = boardElement.insertRow();
                for (let x = 0; x < gameStateToDraw.boardWidth; x++) {
                    const cell = row.insertCell();
                    cell.id = 'cell-' + x + '-' + y;
                }
            }

            for (const key in gameStateToDraw.items) {
                const item = gameStateToDraw.items[key];
                const cell = document.getElementById('cell-' + item.pos.x + '-' + item.pos.y);
                if (cell) {
                    cell.classList.add('item');
                    cell.textContent = '💎'; 
                }
            }
            
            let scoresHTML = "";
            for (const id in gameStateToDraw.players) {
                const player = gameStateToDraw.players[id];
                // Adicionado try-catch para o caso de player.pos não estar definido ainda
                try {
                    const cell = document.getElementById('cell-' + player.pos.x + '-' + player.pos.y);
                    if (cell) {
                        cell.classList.add('player');
                        cell.textContent = player.id.substring(0,2); 
                        if (player.id === myPlayerId) {
                            cell.classList.add('self');
                        }
                    }
                } catch (e) {
                    clientLog("Erro ao desenhar jogador " + player.id + ": " + e.message);
                }
                scoresHTML += player.id.substring(0,8) + "...: " + player.score + "\n";
            }
            scoresElement.textContent = scoresHTML;

            if (gameStateToDraw.gameOver) {
                gameOverMsgElement.textContent = "FIM DE JOGO! Vencedor(es): " + gameStateToDraw.winnerId;
                gameOverMsgElement.style.display = 'block'; 
                resetButton.style.display = 'inline-block'; 
            } else {
                gameOverMsgElement.style.display = 'none'; 
                resetButton.style.display = 'none'; 
            }
        }

        ws.onopen = function(event) {
            clientLog("Conectado ao servidor WebSocket.");
        };

        ws.onmessage = function(event) {
            const serverMsg = JSON.parse(event.data);
            
            if (serverMsg.type === "welcome") {
                myPlayerId = serverMsg.payload.playerId;
                myIdElement.textContent = myPlayerId.substring(0,8) + "..."; 
                clientLog("Bem-vindo! Seu ID: " + myPlayerId + ". Aguardando estado completo do jogo...");
            } else if (serverMsg.type === "full_state") {
                clientLog("Recebido Estado Completo do Jogo.");
                localGameState.players = serverMsg.payload.players || {};
                localGameState.items = serverMsg.payload.items || {};
                localGameState.boardWidth = serverMsg.payload.boardWidth;
                localGameState.boardHeight = serverMsg.payload.boardHeight;
                localGameState.gameOver = serverMsg.payload.gameOver;
                localGameState.winnerId = serverMsg.payload.winnerId;
                drawBoard(localGameState); 
            } else if (serverMsg.type === "delta_update") {
                const delta = serverMsg.payload;

                if (delta.playersUpdated) {
                    for (const playerId in delta.playersUpdated) {
                        const pDelta = delta.playersUpdated[playerId];
                        if (!localGameState.players[playerId]) { 
                            localGameState.players[playerId] = { id: playerId, score: 0, pos: pDelta.pos || {x:0, y:0} }; 
                            // clientLog("Novo jogador via delta: " + playerId);
                        }
                        if (pDelta.pos) {
                            localGameState.players[playerId].pos = pDelta.pos;
                        }
                        if (pDelta.score !== undefined && pDelta.score !== null) { 
                            localGameState.players[playerId].score = pDelta.score;
                        }
                    }
                }

                if (delta.playersRemoved) {
                    delta.playersRemoved.forEach(playerId => {
                        delete localGameState.players[playerId];
                        // clientLog("Jogador removido via delta: " + playerId);
                    });
                }
                
                if (delta.itemsAdded && delta.itemsAdded.length >= 0) { // Checa se existe e se tem itens (pode ser array vazio no reset)
                    // clientLog("Itens adicionados/resetados via delta. Total: " + delta.itemsAdded.length);
                    localGameState.items = {}; 
                    delta.itemsAdded.forEach(item => {
                        const itemKey = item.pos.x + "," + item.pos.y; 
                        localGameState.items[itemKey] = item;
                    });
                }
                
                if (delta.itemsRemoved) {
                    delta.itemsRemoved.forEach(itemKey => { 
                        delete localGameState.items[itemKey];
                    });
                }

                if (delta.gameStatus) {
                    localGameState.gameOver = delta.gameStatus.gameOver;
                    localGameState.winnerId = delta.gameStatus.winnerId;
                }
                
                drawBoard(localGameState); 
            }
        };

        ws.onclose = function(event) {
            clientLog("Desconectado do servidor WebSocket. Código: " + event.code + " Razão: " + event.reason);
            gameOverMsgElement.textContent = "DESCONECTADO DO SERVIDOR";
            gameOverMsgElement.style.display = 'block';
        };

        ws.onerror = function(error) {
            clientLog("Erro no WebSocket: " + JSON.stringify(error));
        };

        function sendMove(direction) {
            if (!ws || ws.readyState !== WebSocket.OPEN) {
                clientLog("WebSocket não está aberto para enviar movimento.");
                return;
            }
            if (!myPlayerId) {
                clientLog("Meu ID de jogador ainda não está definido. Não é possível enviar movimento.");
                return;
            }
            ws.send(JSON.stringify({ action: 'move', direction: direction }));
        }
        
        resetButton.onclick = function() {
            if (!ws || ws.readyState !== WebSocket.OPEN) return;
            ws.send(JSON.stringify({ action: 'reset_game_request' }));
            clientLog("Solicitação de reset do jogo enviada.");
        };

        document.addEventListener('keydown', function(event) {
            if (!ws || ws.readyState !== WebSocket.OPEN) return;
            let direction = null;
            switch (event.key) {
                case 'w': case 'W': case 'ArrowUp': direction = 'up'; break;
                case 's': case 'S': case 'ArrowDown': direction = 'down'; break;
                case 'a': case 'A': case 'ArrowLeft': direction = 'left'; break;
                case 'd': case 'D': case 'ArrowRight': direction = 'right'; break;
            }
            if (direction) {
                sendMove(direction);
                event.preventDefault();
            }
        });
    </script>
</body>
</html>
`
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, html)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
		log.Printf("Variável PORT não definida, usando porta padrão: %s", port)
	}

	go gameLoop()

	log.Printf("Servidor Go Diamond Collector iniciando na porta :%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Erro ao iniciar servidor ListenAndServe: %v", err)
	}
}

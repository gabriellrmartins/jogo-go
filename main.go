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

type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type Player struct {
	ID       string          `json:"id"`
	Pos      Point           `json:"pos"`
	Score    int             `json:"score"`
	conn     *websocket.Conn `json:"-"`
	sendChan chan []byte     `json:"-"`
	IsActive bool            `json:"isActive"`
}

type Item struct {
	ID  string `json:"id"`
	Pos Point  `json:"pos"`
}

type GameState struct {
	Players     map[string]*Player `json:"players"`
	Items       map[string]*Item   `json:"items"`
	BoardWidth  int                `json:"boardWidth"`
	BoardHeight int                `json:"boardHeight"`
	GameOver    bool               `json:"gameOver"`
	WinnerID    string             `json:"winnerId,omitempty"`
	mu          sync.Mutex         // Mutex para proteger o acesso concorrente ao estado
}

type ClientMessage struct {
	Action    string `json:"action"`
	Direction string `json:"direction"`
}

var game = &GameState{
	Players:     make(map[string]*Player),
	Items:       make(map[string]*Item),
	BoardWidth:  BoardWidth,
	BoardHeight: BoardHeight,
	GameOver:    false,
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// initializeItems coloca os itens no tabuleiro em posições aleatórias
func (gs *GameState) initializeItems() {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	gs.Items = make(map[string]*Item)
	for i := 0; i < NumItems; i++ {
		var itemPos Point
		uniquePos := false
		for !uniquePos { // Garante que o item não sobreponha outro item ou jogador inicial
			itemPos = Point{X: rand.Intn(BoardWidth), Y: rand.Intn(BoardHeight)}
			key := fmt.Sprintf("%d,%d", itemPos.X, itemPos.Y)
			if _, exists := gs.Items[key]; !exists {
				playerOccupies := false
				for _, p := range gs.Players { // Verifica se algum jogador já está lá
					if p.Pos.X == itemPos.X && p.Pos.Y == itemPos.Y {
						playerOccupies = true
						break
					}
				}
				if !playerOccupies {
					uniquePos = true
				}
			}
		}
		itemID := "item_" + strconv.Itoa(i)
		itemKey := fmt.Sprintf("%d,%d", itemPos.X, itemPos.Y)
		gs.Items[itemKey] = &Item{ID: itemID, Pos: itemPos}
	}

	gs.GameOver = false
	gs.WinnerID = ""

	for _, player := range gs.Players {
		if player.IsActive {
			player.Score = 0
		}
	}

	log.Printf("Jogo iniciado/resetado com %d itens. Pontuações dos jogadores zeradas.", len(gs.Items))
}

func (gs *GameState) addPlayer(id string, conn *websocket.Conn) *Player {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	var startPos Point
	uniquePos := false
	for !uniquePos { // Encontra uma posição inicial única
		startPos = Point{X: rand.Intn(BoardWidth), Y: rand.Intn(BoardHeight)}
		occupied := false
		for _, p := range gs.Players {
			if p.Pos.X == startPos.X && p.Pos.Y == startPos.Y {
				occupied = true
				break
			}
		}
		if occupied {
			continue
		}
		itemKey := fmt.Sprintf("%d,%d", startPos.X, startPos.Y)
		if _, exists := gs.Items[itemKey]; exists { // Não nascer em cima de um item
			occupied = true
		}
		if !occupied {
			uniquePos = true
		}
	}

	player := &Player{
		ID:       id,
		Pos:      startPos,
		Score:    0,
		conn:     conn,
		sendChan: make(chan []byte, 256), // Canal bufferizado para mensagens de saída
		IsActive: true,
	}
	gs.Players[id] = player
	log.Printf("Jogador %s entrou em (%d, %d). Total de jogadores: %d", id, player.Pos.X, player.Pos.Y, len(gs.Players))
	return player
}

func (gs *GameState) removePlayer(id string) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	if player, ok := gs.Players[id]; ok {
		player.IsActive = false // Marca como inativo
		close(player.sendChan)  // Fecha o canal de envio, sinalizando para a goroutine 'writer' parar
		delete(gs.Players, id)  // Remove do mapa principal
		log.Printf("Jogador %s removido. Total de jogadores: %d", id, len(gs.Players))
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
		return // Direção inválida
	}

	player.Pos = newPos // Atualiza a posição do jogador

	// Verifica coleta de item
	itemKey := fmt.Sprintf("%d,%d", newPos.X, newPos.Y)
	if item, exists := gs.Items[itemKey]; exists {
		player.Score++
		delete(gs.Items, itemKey) // Remove o item do jogo
		log.Printf("Jogador %s coletou item %s. Pontuação: %d. Itens restantes: %d", player.ID, item.ID, player.Score, len(gs.Items))

		if len(gs.Items) == 0 { // Verifica se o jogo acabou
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
				gs.WinnerID = fmt.Sprintf("%v", winners) // Pode haver empates
				log.Printf("FIM DE JOGO! Vencedor(es): %s com %d pontos.", gs.WinnerID, winnerScore)
			} else {
				log.Printf("FIM DE JOGO! Nenhum jogador ativo para declarar vencedor.")
			}
		}
	}
}

// broadcastGameState envia o estado atual do jogo para todos os jogadores ativos
func (gs *GameState) broadcastGameState() {
	gs.mu.Lock() // Protege leitura do estado para criar o snapshot

	playersToSend := make(map[string]interface{})
	for id, p := range gs.Players {
		if p.IsActive {
			playersToSend[id] = struct {
				ID    string `json:"id"`
				Pos   Point  `json:"pos"`
				Score int    `json:"score"`
			}{p.ID, p.Pos, p.Score}
		}
	}

	itemsToSend := make(map[string]*Item)
	for id, i := range gs.Items {
		itemsToSend[id] = i
	}

	stateSnapshot := struct {
		Players     map[string]interface{} `json:"players"`
		Items       map[string]*Item       `json:"items"`
		BoardWidth  int                    `json:"boardWidth"`
		BoardHeight int                    `json:"boardHeight"`
		GameOver    bool                   `json:"gameOver"`
		WinnerID    string                 `json:"winnerId,omitempty"`
	}{
		Players:     playersToSend,
		Items:       itemsToSend,
		BoardWidth:  gs.BoardWidth,
		BoardHeight: gs.BoardHeight,
		GameOver:    gs.GameOver,
		WinnerID:    gs.WinnerID,
	}
	gs.mu.Unlock() // Libera o mutex assim que a cópia é feita

	message, err := json.Marshal(stateSnapshot)
	if err != nil {
		log.Printf("Erro ao serializar estado do jogo: %v", err)
		return
	}

	// Coleta jogadores ativos para enviar a mensagem (para evitar segurar o lock durante os envios)
	activePlayersToSendTo := []*Player{}
	gs.mu.Lock()
	for _, player := range gs.Players {
		if player.IsActive {
			activePlayersToSendTo = append(activePlayersToSendTo, player)
		}
	}
	gs.mu.Unlock()

	for _, player := range activePlayersToSendTo {
		select {
		case player.sendChan <- message:
		default:
			log.Printf("Canal de envio do jogador %s cheio. Descartando mensagem de estado.", player.ID)
		}
	}
}

// writer é uma goroutine que envia mensagens do `sendChan` para o WebSocket do jogador
func writer(player *Player) {
	defer func() {
		player.conn.Close() // Fecha a conexão ao sair
		log.Printf("Escritor para o jogador %s encerrado.", player.ID)
	}()

	for message := range player.sendChan { // Loop até o canal ser fechado
		if err := player.conn.WriteMessage(websocket.TextMessage, message); err != nil {
			log.Printf("Erro ao escrever para jogador %s: %v", player.ID, err)
			return // Encerra se houver erro de escrita (conexão provavelmente perdida)
		}
	}
}

// reader é uma goroutine que lê mensagens do WebSocket do jogador
func reader(player *Player) {
	defer func() {
		log.Printf("Leitor para o jogador %s encerrando. Realizando limpeza.", player.ID)
		game.removePlayer(player.ID) // Remove o jogador do jogo (isso fechará sendChan, parando o writer)
	}()

	player.conn.SetReadLimit(512) // Define um limite de tamanho para mensagens lidas
	for {
		messageType, p, err := player.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Erro de conexão inesperado para jogador %s: %v", player.ID, err)
			} else {
				log.Printf("Jogador %s desconectado: %v", player.ID, err)
			}
			break // Sai do loop em caso de erro (dispara o defer)
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
				game.initializeItems()
			}
		}
	}
}

// wsHandler lida com novas conexões WebSocket
func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Falha ao fazer upgrade da conexão para WebSocket: %v", err)
		return
	}

	playerID := uuid.NewString() // Geração de ID com UUID
	log.Printf("Novo jogador tentando conectar com ID gerado: %s", playerID)

	player := game.addPlayer(playerID, conn)

	go writer(player)
	go reader(player)

	// Enviar uma mensagem inicial de "boas-vindas" com o ID do jogador
	welcomeMsg := map[string]string{"type": "welcome", "playerId": player.ID}
	welcomeData, _ := json.Marshal(welcomeMsg)
	select {
	case player.sendChan <- welcomeData:
	default:
		log.Printf("Não foi possível enviar mensagem de boas-vindas para %s", player.ID)
	}
}

// gameLoop é a goroutine principal do jogo que periodicamente envia o estado
func gameLoop() {
	ticker := time.NewTicker(GameTickDelay)
	defer ticker.Stop()

	for {
		<-ticker.C
		game.broadcastGameState()
	}
}

func main() {
	rand.Seed(time.Now().UnixNano())
	game.initializeItems()

	http.HandleFunc("/ws", wsHandler)                                   // Endpoint WebSocket
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { // Servir o cliente HTML
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
            --primary-bg: #f4f7f6;
            --secondary-bg: #ffffff;
            --accent-color: #3498db; /* Azul suave */
            --accent-hover: #2980b9;
            --text-color: #333333;
            --border-color: #dddddd;
            --item-bg: #f1c40f; /* Dourado para itens */
            --player-bg: #87ceeb; /* Azul céu para jogador */
            --self-player-bg: #5dade2; /* Azul mais forte para jogador local */
            --shadow-color: rgba(0,0,0,0.1);
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
            font-weight: 300;
        }
        #game-description {
            background-color: var(--secondary-bg);
            padding: 15px 20px;
            border-radius: 8px;
            margin-bottom: 25px;
            max-width: 700px;
            box-shadow: 0 2px 4px var(--shadow-color);
            text-align: left;
        }
        #game-description h2 {
            margin-top: 0;
            color: var(--accent-color);
            font-size: 1.4em;
            font-weight: 400;
            border-bottom: 1px solid var(--border-color);
            padding-bottom: 0.5em;
            margin-bottom: 0.8em;
        }
        #game-description p, #game-description ul {
            font-size: 0.95em;
            margin-bottom: 0.8em;
        }
        #game-description ul {
            list-style-type: disc;
            padding-left: 20px;
        }
        #game-description strong {
            color: var(--accent-color);
        }
        #game-container { 
            display: flex; 
            flex-wrap: wrap; 
            gap: 25px; 
            justify-content: center;
            width: 100%;
        }
        #board-wrapper { 
            width: auto; /* Ajusta-se ao conteúdo */
            max-width: 100%; /* Não ultrapassa a tela */
            overflow-x: auto; 
            display: flex;
            justify-content: center; 
            padding: 5px; /* Pequeno padding para não cortar a borda do tabuleiro */
            background-color: var(--secondary-bg);
            border-radius: 8px;
            box-shadow: 0 2px 4px var(--shadow-color);
        }
        #board {
            border-collapse: collapse;
            font-family: monospace;
            table-layout: fixed; 
            border: 1px solid var(--border-color); /* Borda mais suave */
        }
        #board td {
            border: 1px solid #e7e7e7; /* Linhas de grade ainda mais suaves */
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
            0% { transform: scale(0.9); }
            50% { transform: scale(1.05); }
            100% { transform: scale(0.9); }
        }
        #info { 
            text-align: left; 
            padding: 20px; 
            border: 1px solid var(--border-color); 
            background-color: var(--secondary-bg); 
            border-radius: 8px; 
            min-width: 280px; 
            box-shadow: 0 2px 4px var(--shadow-color);
        }
        #info h3 { 
            margin-top: 0; 
            margin-bottom: 10px; 
            font-size: 1.3em;
            color: var(--accent-color);
            font-weight: 400;
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
            min-width: 80px; /* Largura mínima para botões de controle */
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
            background-color: #ffdddd;
            border: 1px solid #ffaaaa;
            color: #d8000c; 
            font-weight:bold; 
            margin-bottom: 15px; 
            font-size: 1.2em; 
            border-radius: 5px;
            text-align: center;
            display: none; /* Escondido por padrão, JS mostra */
        }
        #resetButton {
            background-color: #5bc0de; /* Azul informativo */
        }
        #resetButton:hover {
            background-color: #31b0d5;
        }

        /* === Media Queries para Responsividade === */
        @media (max-width: 768px) {
            body { padding: 15px; }
            h1 { font-size: 1.8em; }
            #game-description { width: 95%; padding: 15px; margin-bottom: 20px;}
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
                max-width: 480px; 
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
                display: flex; /* Para centralizar ícone/texto */
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
        <button id="btn-up" onclick="sendMove('up')" title="Mover para Cima (W ou Seta para Cima)">&#x25B2;</button> <br> 
        <button id="btn-left" onclick="sendMove('left')" title="Mover para Esquerda (A ou Seta para Esquerda)">&#x25C0;</button> <span id="btn-placeholder"></span> 
        <button id="btn-right" onclick="sendMove('right')" title="Mover para Direita (D ou Seta para Direita)">&#x25B6;</button> <br> 
        <button id="btn-down" onclick="sendMove('down')" title="Mover para Baixo (S ou Seta para Baixo)">&#x25BC;</button> </div>
    <div id="log-container">
      <h4>Log de Eventos (Debug):</h4>
      <pre id="log"></pre>
    </div>

    <script>
        const boardElement = document.getElementById('board');
        const scoresElement = document.getElementById('scores');
        const logElement = document.getElementById('log'); // Log na tela
        const myIdElement = document.getElementById('my-id');
        const gameOverMsgElement = document.getElementById('game-over-msg');
        const resetButton = document.getElementById('resetButton');

        const wsProtocol = window.location.protocol === "https:" ? "wss:" : "ws:";
        const ws = new WebSocket(wsProtocol + "//" + window.location.host + "/ws");
        let myPlayerId = null;

        function clientLog(message) {
            console.log(message); // Log no console do navegador
            const now = new Date();
            const timeString = now.getHours().toString().padStart(2, '0') + ':' + 
                               now.getMinutes().toString().padStart(2, '0') + ':' + 
                               now.getSeconds().toString().padStart(2, '0');
            if (logElement.textContent.length > 2000) { 
                logElement.textContent = logElement.textContent.substring(0,1500);
            }
            logElement.textContent = timeString + ": " + message + "\n" + logElement.textContent;
        }

        function drawBoard(gameState) {
            boardElement.innerHTML = ''; 
            for (let y = 0; y < gameState.boardHeight; y++) {
                const row = boardElement.insertRow();
                for (let x = 0; x < gameState.boardWidth; x++) {
                    const cell = row.insertCell();
                    cell.id = 'cell-' + x + '-' + y;
                }
            }

            for (const key in gameState.items) {
                const item = gameState.items[key];
                const cell = document.getElementById('cell-' + item.pos.x + '-' + item.pos.y);
                if (cell) {
                    cell.classList.add('item');
                    cell.textContent = '💎'; 
                }
            }
            
            let scoresHTML = "";
            for (const id in gameState.players) {
                const player = gameState.players[id];
                const cell = document.getElementById('cell-' + player.pos.x + '-' + player.pos.y);
                if (cell) {
                    cell.classList.add('player');
                    cell.textContent = player.id.substring(0,2); 
                    if (player.id === myPlayerId) {
                        cell.classList.add('self');
                    }
                }
                scoresHTML += player.id.substring(0,8) + "...: " + player.score + "\n";
            }
            scoresElement.textContent = scoresHTML;

            if (gameState.gameOver) {
                gameOverMsgElement.textContent = "FIM DE JOGO! Vencedor(es): " + gameState.winnerId;
                resetButton.style.display = 'inline-block'; // Mostrar botão
            } else {
                gameOverMsgElement.textContent = "";
                resetButton.style.display = 'none'; // Esconder botão
            }
        }

        ws.onopen = function(event) {
            clientLog("Conectado ao servidor WebSocket.");
        };

        ws.onmessage = function(event) {
            const data = JSON.parse(event.data);
            
            if (data.type === "welcome") {
                myPlayerId = data.playerId;
                myIdElement.textContent = myPlayerId.substring(0,8) + "..."; // Mostra ID abreviado
                clientLog("Meu ID de jogador definido: " + myPlayerId);
                return; 
            }
            drawBoard(data);
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

	// Determina a porta para escutar
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // Porta padrão se PORT não estiver definida
		log.Printf("Variável PORT não definida, usando porta padrão: %s", port)
	}

	go gameLoop() // Inicia o loop principal do jogo em uma goroutine separada

	log.Printf("Servidor Go Diamond Collector iniciando na porta :%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Erro ao iniciar servidor ListenAndServe: %v", err) // Usar log.Fatalf para sair em caso de erro fatal
	}
}

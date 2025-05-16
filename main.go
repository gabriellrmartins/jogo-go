package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os" // Importado para usar os.Getenv("PORT")
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid" // Importar UUID
	"github.com/gorilla/websocket"
)

const (
	BoardWidth    = 20                     // Largura do tabuleiro
	BoardHeight   = 15                     // Altura do tabuleiro
	NumItems      = 15                     // Número inicial de itens
	GameTickDelay = 150 * time.Millisecond // Intervalo para enviar atualizações de estado
)

// Point representa uma coordenada no tabuleiro
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Player representa um jogador no jogo
type Player struct {
	ID       string          `json:"id"`
	Pos      Point           `json:"pos"`
	Score    int             `json:"score"`
	conn     *websocket.Conn `json:"-"`        // Conexão WebSocket (não serializada para JSON)
	sendChan chan []byte     `json:"-"`        // Canal para enviar mensagens para este jogador
	IsActive bool            `json:"isActive"` // Indica se o jogador está ativo
}

// Item representa um item coletável
type Item struct {
	ID  string `json:"id"`
	Pos Point  `json:"pos"`
}

// GameState armazena o estado atual do jogo
type GameState struct {
	Players     map[string]*Player `json:"players"`
	Items       map[string]*Item   `json:"items"` // Chave: "x,y" para fácil busca
	BoardWidth  int                `json:"boardWidth"`
	BoardHeight int                `json:"boardHeight"`
	GameOver    bool               `json:"gameOver"`
	WinnerID    string             `json:"winnerId,omitempty"`
	mu          sync.Mutex         // Mutex para proteger o acesso concorrente ao estado
}

// ClientMessage é a estrutura para mensagens enviadas pelo cliente
type ClientMessage struct {
	Action    string `json:"action"`    // Ex: "move"
	Direction string `json:"direction"` // Ex: "up", "down", "left", "right"
}

// Variável global para o estado do jogo
var game = &GameState{
	Players:     make(map[string]*Player),
	Items:       make(map[string]*Item),
	BoardWidth:  BoardWidth,
	BoardHeight: BoardHeight,
	GameOver:    false,
}

// Upgrader do WebSocket (permite todas as origens para simplicidade)
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Permite todas as origens para este exemplo
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
	log.Printf("Jogo iniciado/resetado com %d itens.", len(gs.Items))
}

// addPlayer adiciona um novo jogador ao jogo
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

// removePlayer remove um jogador do jogo
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

// handlePlayerMove processa o movimento de um jogador
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
		// Conteúdo HTML com correções de CSS e meta charset
		html := `
<!DOCTYPE html>
<html lang="pt-BR">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Go Concurrent Game</title>
    <style>
        body { font-family: Arial, sans-serif; display: flex; flex-direction: column; align-items: center; margin: 0; padding: 20px; background-color: #f0f0f0; }
        h1 { margin-bottom: 10px;}
        p { margin-bottom: 15px; }
        #game-container { display: flex; flex-wrap: wrap; gap: 20px; justify-content: center;}
        #board {
            border-collapse: collapse;
            font-family: monospace;
            table-layout: fixed; /* Importante para células respeitarem a largura */
            border: 2px solid #333;
        }
        #board td {
            border: 1px solid #ccc;
            width: 28px;   /* Ajustado para melhor visualização */
            height: 28px;  /* Ajustado para melhor visualização */
            text-align: center;
            vertical-align: middle;
            font-size: 14px; /* Ajustado para melhor visualização do emoji/texto */
            overflow: hidden; 
            box-sizing: border-box; 
            white-space: nowrap; 
            line-height: 26px; /* (height - border), ajuda a centralizar */
        }
        .player { background-color: lightblue; border-radius: 50%; }
        .item { background-color: gold; }
        .self { font-weight: bold; background-color: #66ccff; box-shadow: 0 0 3px 2px cyan; } /* Destaque para o jogador local */
        #info { 
            margin-top: 0px; /* Ajustado */
            text-align: left; 
            padding: 15px; 
            border: 1px solid #ddd; 
            background-color: #fff; 
            border-radius: 5px;
            min-width: 250px; /* Largura mínima para o painel de informações */
        }
        #info h3 { margin-top: 0; margin-bottom: 5px; }
        #info pre { margin-top: 5px; margin-bottom: 10px; white-space: pre-wrap; }
        #controls { margin-top: 20px; text-align: center; }
        #controls button { 
            padding: 10px 15px; 
            margin: 5px; 
            font-size: 16px; 
            cursor: pointer; 
            border: 1px solid #ccc;
            border-radius: 4px;
            background-color: #e9e9e9;
        }
        #controls button:hover { background-color: #dcdcdc; }
        #log-container { width: 100%; max-width: 600px; margin-top:20px; }
        #log { 
            font-size:0.8em; 
            max-height: 100px; 
            overflow-y: scroll; 
            border: 1px solid #eee; 
            padding:10px; 
            background-color: #fff;
            white-space: pre-wrap; 
            word-break: break-all;
        }
        #game-over-msg { color:red; font-weight:bold; margin-bottom: 10px; }
    </style>
</head>
<body>
    <h1>Go Concurrent Game</h1>
    <p>Use W, A, S, D ou as setas para mover. Abra em várias abas!</p>
    <div id="game-container">
        <table id="board"></table>
        <div id="info">
            <h3>Seu ID: <span id="my-id">---</span></h3>
            <h3>Pontuações:</h3>
            <pre id="scores"></pre>
            <div id="game-over-msg"></div>
            <button id="resetButton" style="display:none;">Resetar Jogo (se GameOver)</button>
        </div>
    </div>
    <div id="controls">
        <button onclick="sendMove('up')" title="Mover para Cima (W ou Seta para Cima)">Up (W)</button><br>
        <button onclick="sendMove('left')" title="Mover para Esquerda (A ou Seta para Esquerda)">Left (A)</button>
        <button onclick="sendMove('down')" title="Mover para Baixo (S ou Seta para Baixo)">Down (S)</button>
        <button onclick="sendMove('right')" title="Mover para Direita (D ou Seta para Direita)">Right (D)</button>
    </div>
    <div id="log-container">
      <h4>Log de Eventos do Cliente (para debug):</h4>
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
            logElement.textContent = timeString + ": " + message + "\n" + logElement.textContent.substring(0, 2000);
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
                myIdElement.textContent = myPlayerId;
                clientLog("Meu ID de jogador definido: " + myPlayerId);
                return; 
            }
            drawBoard(data);
        };

        ws.onclose = function(event) {
            clientLog("Desconectado do servidor WebSocket. Código: " + event.code + " Razão: " + event.reason);
            gameOverMsgElement.textContent = "DESCONECTADO DO SERVIDOR";
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8") // Garante que o header HTTP também indique UTF-8
		fmt.Fprint(w, html)
	})

	// Determina a porta para escutar
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // Porta padrão se PORT não estiver definida
		log.Printf("Variável PORT não definida, usando porta padrão: %s", port)
	}

	go gameLoop() // Inicia o loop principal do jogo em uma goroutine separada

	log.Printf("Servidor Go Concurrent Game iniciando na porta :%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Erro ao iniciar servidor ListenAndServe: %v", err) // Usar log.Fatalf para sair em caso de erro fatal
	}
}

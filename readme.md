# Go Concurrent Game - Jogo Concorrente em Go - https://jogo-go.onrender.com/

Este é um jogo multiplayer simples de coleta de itens desenvolvido em Go, demonstrando conceitos de programação concorrente e distribuída utilizando WebSockets para comunicação em tempo real.

## Visão Geral

O jogo consiste em múltiplos jogadores movendo-se em um tabuleiro 2D compartilhado. Itens colecionáveis são espalhados pelo tabuleiro. Os jogadores controlam seus personagens para coletar esses itens. O jogo termina quando todos os itens são coletados, e o jogador com a maior pontuação vence. Todos os jogadores veem as posições e pontuações uns dos outros em tempo real.

## Funcionalidades

* **Multiplayer em Tempo Real:** Vários jogadores podem se conectar e jogar simultaneamente.
* **Estado Compartilhado:** Todos os jogadores interagem com o mesmo tabuleiro e veem as mesmas informações.
* **Comunicação via WebSockets:** Para atualizações instantâneas de estado entre o servidor (Go) e os clientes (navegadores).
* **Lógica de Concorrência:** Utiliza goroutines para lidar com cada cliente e mutexes para proteger o acesso ao estado compartilhado do jogo.
* **Interface Simples no Navegador:** Frontend em HTML, CSS e JavaScript para visualização e interação.

## Tecnologias Utilizadas

* **Backend:** Go
    * `net/http` para o servidor web.
    * `github.com/gorilla/websocket` para comunicação WebSocket.
    * `github.com/google/uuid` para geração de IDs de jogador únicos.
    * Goroutines e Mutexes (`sync.Mutex`) para concorrência.
    * Canais Go para comunicação interna (ex: `sendChan` por jogador).
* **Frontend:** HTML, CSS, JavaScript (puro)

## Estrutura do Projeto
go-concurrent-game/
├── .gitignore       # Arquivos e pastas a serem ignorados pelo Git
├── go.mod           # Arquivo de módulo Go (define dependências)
├── main.go          # Código fonte principal do servidor e lógica do jogo
└── README.md        # Este arquivo

## Pré-requisitos

* Go (versão 1.18 ou superior recomendada) instalado: [https://golang.org/dl/](https://golang.org/dl/)
* Um navegador web moderno.

## Passo a Passo para Execução

1.  **Clonar o Repositório (ou criar os arquivos manualmente):**
    Se você estiver clonando de um repositório Git:
    ```bash
    git clone [https://github.com/SEU_USUARIO_GITHUB/go-concurrent-game.git](https://github.com/SEU_USUARIO_GITHUB/go-concurrent-game.git)
    cd go-concurrent-game
    ```
    Se estiver criando manualmente, crie a estrutura de pastas acima e cole o conteúdo de cada arquivo.

2.  **Resolver Dependências:**
    Navegue até o diretório raiz do projeto (`go-concurrent-game`) e execute:
    ```bash
    go mod tidy 
    ```
    Este comando irá baixar as dependências listadas no `go.mod` (como `gorilla/websocket` e `google/uuid`).

3.  **Executar o Servidor:**
    Ainda no diretório raiz, execute:
    ```bash
    go run main.go
    ```
    Você deverá ver uma mensagem no console indicando que o servidor foi iniciado, por exemplo:
    `Servidor Go Concurrent Game iniciado em [http://localhost:8080] ou (https://jogo-go.onrender.com/)`

4.  **Acessar o Jogo:**
    Abra um navegador web e acesse o endereço: `[http://localhost:8080] ou (https://jogo-go.onrender.com/)`

5.  **Jogar:**
    * Abra múltiplas abas ou janelas do navegador no mesmo endereço para simular múltiplos jogadores.
    * Cada aba representará um jogador diferente.

## Explicação do Algoritmo e Funcionamento

### Backend (Go - `main.go`)

1.  **Servidor HTTP e WebSocket:**
    * Um servidor HTTP é iniciado na porta `:8080`.
    * A rota `/` serve o cliente HTML (interface do jogo).
    * A rota `/ws` é o endpoint WebSocket. Quando um cliente se conecta a `/ws`, a conexão HTTP é atualizada para uma conexão WebSocket.

2.  **Gerenciamento de Estado do Jogo:**
    * **`GameState` (struct):** Contém o estado global do jogo:
        * `Players`: Um mapa de jogadores conectados (`map[string]*Player`).
        * `Items`: Um mapa dos itens no tabuleiro (`map[string]*Item`).
        * Dimensões do tabuleiro, status de `GameOver`, `WinnerID`.
        * `mu (sync.Mutex)`: Um mutex para proteger o acesso concorrente ao `GameState`, garantindo que apenas uma goroutine modifique o estado por vez, evitando race conditions.
    * **`Player` (struct):** Representa um jogador com ID, posição (`Pos`), pontuação (`Score`), a conexão WebSocket (`conn`) e um canal (`sendChan`) para enviar mensagens específicas para ele.
    * **`Item` (struct):** Representa um item colecionável com ID e posição.
    * A variável global `game` instância o `GameState`.

3.  **Conexões de Jogadores (`wsHandler` e `addPlayer`):**
    * Quando um novo cliente se conecta ao endpoint `/ws`, `wsHandler` é chamado.
    * Um ID único é gerado para o jogador usando `uuid.NewString()`.
    * `addPlayer` adiciona o novo jogador ao mapa `game.Players` (protegido pelo mutex).
    * Duas goroutines são iniciadas para cada jogador conectado:
        * `reader(player)`: Lê mensagens (comandos de movimento) vindas do cliente através do WebSocket.
        * `writer(player)`: Envia mensagens (atualizações de estado do jogo) do servidor para o cliente através do WebSocket, usando o `player.sendChan`.
    * Uma mensagem de "welcome" com o ID do jogador é enviada ao cliente recém-conectado.

4.  **Lógica de Movimentação e Coleta (`handlePlayerMove`):**
    * Chamada pela goroutine `reader` quando um comando de movimento é recebido.
    * Adquire o lock (`game.mu.Lock()`) para modificar o estado do jogo com segurança.
    * Valida o movimento (limites do tabuleiro).
    * Atualiza a posição do jogador.
    * Verifica se a nova posição contém um item. Se sim, o jogador coleta o item (pontuação aumenta, item é removido do `game.Items`).
    * Verifica se todos os itens foram coletados para definir `game.GameOver`.
    * Libera o lock (`game.mu.Unlock()`).

5.  **Comunicação em Tempo Real (`broadcastGameState`, `reader`, `writer`):**
    * **`reader` Goroutine:** Para cada jogador, lê continuamente as mensagens do WebSocket. Se for um movimento, chama `handlePlayerMove`. Também lida com desconexões.
    * **`writer` Goroutine:** Para cada jogador, lê continuamente de `player.sendChan`. Quando há uma mensagem nesse canal, ela é enviada ao cliente via WebSocket. Isso desacopla o envio da lógica principal.
    * **`broadcastGameState`:**
        * Cria um "snapshot" seguro do estado atual do jogo (protegido por mutex).
        * Serializa esse snapshot para JSON.
        * Envia essa mensagem JSON para o `sendChan` de cada jogador ativo. A goroutine `writer` de cada jogador se encarrega de transmitir efetivamente. O envio para o canal é não-bloqueante para evitar que um cliente lento trave o broadcast para os demais.

6.  **Loop Principal do Jogo (`gameLoop`):**
    * Roda em uma goroutine separada.
    * Usa um `time.Ticker` para, em intervalos regulares (`GameTickDelay`), chamar `broadcastGameState`. Isso garante que todos os clientes recebam atualizações periódicas do estado do jogo, mesmo que nenhum jogador tenha realizado uma ação.

### Frontend (HTML, CSS, JavaScript - embutido em `main.go`)

1.  **Estrutura HTML:** Define o layout da página, incluindo o título, o tabuleiro (`<table id="board">`), a área de informações (`<div id="info">`), controles e uma área de log.
2.  **CSS:** Estiliza os elementos da página para uma apresentação visual básica.
3.  **JavaScript:**
    * **Conexão WebSocket:** Estabelece uma conexão com o endpoint `/ws` do servidor.
    * **Identificação do Jogador:** Ao receber uma mensagem do tipo `"welcome"` do servidor, armazena o `myPlayerId` para identificar o jogador local.
    * **Envio de Ações:** Captura eventos de teclado (W, A, S, D, Setas) e cliques nos botões para enviar mensagens de movimento (`{action: "move", direction: "..."}`) ao servidor via WebSocket.
    * **Recebimento e Renderização:**
        * `ws.onmessage`: Manipula mensagens recebidas do servidor.
        * Se a mensagem não for "welcome", é um estado de jogo completo.
        * `drawBoard(gameState)`: Limpa o tabuleiro e redesenha todos os jogadores e itens com base no `gameState` recebido. Destaca o jogador local (`.self`). Atualiza as pontuações e a mensagem de fim de jogo.

### Concorrência

* **Goroutines por Cliente:** Cada cliente WebSocket conectado é gerenciado por duas goroutines dedicadas (`reader` e `writer`), permitindo que o servidor lide com I/O de múltiplos clientes de forma concorrente e independente.
* **Goroutine do Game Loop:** O `gameLoop` roda concorrentemente, gerenciando o "tick" do jogo e o broadcast periódico do estado.
* **Proteção de Dados Compartilhados:** O `sync.Mutex` (`game.mu`) é crucial. Ele serializa o acesso à estrutura `game` (que contém o estado compartilhado), prevenindo condições de corrida (race conditions) quando múltiplas goroutines (ex: vários `handlePlayerMove` ou `broadcastGameState`) tentam ler ou modificar o estado do jogo simultaneamente.
* **Canais para Desacoplamento:** O `sendChan` em cada `Player` permite que a lógica de broadcast (`broadcastGameState`) envie mensagens para os jogadores sem bloquear diretamente na escrita da rede. A goroutine `writer` de cada jogador lida com a escrita de forma independente.

## Como Jogar

1.  Abra o jogo em seu navegador (`[http://localhost:8080] ou (https://jogo-go.onrender.com/)`).
2.  Para adicionar mais jogadores, abra novas abas ou janelas do navegador no mesmo endereço.
3.  Use as teclas **W, A, S, D** ou as **Setas Direcionais** do teclado para mover seu personagem (o que estiver destacado com um estilo diferente, geralmente `.self`).
4.  O objetivo é coletar os itens (representados por `💎`) no tabuleiro.
5.  Mova seu personagem sobre um item para coletá-lo. Sua pontuação aumentará.
6.  O jogo termina quando todos os itens forem coletados. O jogador com a maior pontuação vence.
7.  Se o jogo terminar, um botão "Resetar Jogo" aparecerá para reiniciar a partida.

# Go Concurrent Game - Jogo Concorrente em Go

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
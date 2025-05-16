# Estágio 1: Build
FROM golang:1.23-alpine AS builder
WORKDIR /app
# Copiar go.mod e go.sum primeiro para aproveitar o cache do Docker
COPY go.mod go.sum ./
RUN go mod download
# Copiar o restante do código fonte
COPY . .
# Compilar a aplicação
# -ldflags="-w -s" reduz o tamanho do binário
# CGO_ENABLED=0 para um binário estático
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -v -o /go-concurrent-game .

# Estágio 2: Imagem final
FROM alpine:latest
WORKDIR /root/
# Copiar apenas o binário compilado do estágio de build
COPY --from=builder /go-concurrent-game .
# Documentar a porta que a aplicação usa (não publica a porta)
# A plataforma de hospedagem usará a variável de ambiente PORT
EXPOSE 8080 
# Comando para rodar a aplicação
CMD ["./go-concurrent-game"]
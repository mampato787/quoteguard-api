FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY cmd/ ./cmd/
RUN go build -o quoteguard ./cmd/api

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /app/quoteguard .
RUN mkdir -p /app/data
EXPOSE 8080
CMD ["./quoteguard"]

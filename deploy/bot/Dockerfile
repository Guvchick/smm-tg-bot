FROM golang:1.22-alpine AS build

WORKDIR /src

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/smm-bot ./cmd/bot

FROM alpine:3.20

RUN apk add --no-cache ca-certificates postgresql-client tzdata

WORKDIR /app
COPY --from=build /out/smm-bot /app/smm-bot

ENTRYPOINT ["/app/smm-bot"]

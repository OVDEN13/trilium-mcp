FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/trilium-mcp .

FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates
COPY --from=build /out/trilium-mcp /usr/local/bin/trilium-mcp
ENTRYPOINT ["/usr/local/bin/trilium-mcp"]

# Use official Golang image for building the application
FROM golang:1.24 AS builder

WORKDIR /app

# Copy go modules files first to leverage Docker caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the entire project
COPY . .

# Build the application, setting the output binary name
RUN CGO_ENABLED=0 GOOS=linux go build -o realtime-server ./cmd/main.go

# Use a minimal image for production
FROM alpine:latest

WORKDIR /root/

# Copy the built binary from the builder stage
COPY --from=builder /app/realtime-server .

# Set environment variables
ENV API_GATEWAY_ENDPOINT="https://qbo0hzke23.execute-api.us-east-1.amazonaws.com/dev"
ENV PORT=8080

# Expose the necessary port
EXPOSE 8080

# Run the application
ENTRYPOINT ["/root/realtime-server"]

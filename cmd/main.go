package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

var apiGatewayClient *apigatewaymanagementapi.Client
var apiEndpoint string

func init() {
	// Set API Gateway URL from environment variable
	apiEndpoint = os.Getenv("API_GATEWAY_ENDPOINT")
	if apiEndpoint == "" {
		log.Fatal("API_GATEWAY_ENDPOINT environment variable is not set")
	}

	// Load AWS SDK v2 configuration
	cfg, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion("us-east-1"))
	if err != nil {
		log.Fatalf("Unable to load AWS SDK config: %v", err)
	}

	// Initialize API Gateway Management API client
	apiGatewayClient = apigatewaymanagementapi.NewFromConfig(cfg, func(o *apigatewaymanagementapi.Options) {
		o.BaseEndpoint = aws.String(apiEndpoint)
	})
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Failed to upgrade connection:", err)
		return
	}
	defer conn.Close()

	// Extract connection ID from request
	connectionID := extractConnectionID(r)
	if connectionID == "" {
		log.Println("No connection ID found")
		return
	}

	log.Println("New WebSocket connection:", connectionID)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("Read error:", err)
			break
		}

		log.Printf("Received message from %s: %s\n", connectionID, string(message))

		// Echo message back
		err = sendMessageToClient(connectionID, message)
		if err != nil {
			log.Println("Error sending message:", err)
		}
	}
}

func extractConnectionID(r *http.Request) string {
	// Extract connection ID from URL path
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[len(parts)-1]
}

func sendMessageToClient(connectionID string, message []byte) error {
	ctx := context.TODO()

	input := &apigatewaymanagementapi.PostToConnectionInput{
		ConnectionId: aws.String(connectionID),
		Data:         message,
	}

	_, err := apiGatewayClient.PostToConnection(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

func main() {
	r := mux.NewRouter()
	r.HandleFunc("/ws/{connectionID}", handleWebSocket)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Println("Server listening on port", port)
	err := http.ListenAndServe(":"+port, r)
	if err != nil {
		log.Fatal("ListenAndServe:", err)
	}
}

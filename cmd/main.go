package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/gorilla/websocket"
)

type SimpleContext struct {
	ConnectionID string `json:"connectionId"`
}

type BodyContext struct {
	Action  string `json:"action"`
	Message string `json:"message"`
}

type MessageContext struct {
	Body         BodyContext `json:"body"`
	ConnectionId string      `json:"connectionId"`
}

type WebSocketClient struct {
	Conn *websocket.Conn
	ID   string
}

var (
	clients   = make(map[string]*WebSocketClient)
	clientsMu sync.Mutex
)

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

func eventListener(connectionID string, client *websocket.Conn) {
	for {
		_, message, err := client.ReadMessage()
		if err != nil {
			log.Printf("error: %v", err)
			break
		}
		sendMessageToClient(connectionID, message)

		log.Printf("message received: %s", message)
	}
}

func connectToOpenAI(connectionID string) error {
	clientsMu.Lock()
	defer clientsMu.Unlock()

	wssEndpoint := os.Getenv("OPENAI_WSS_URL")
	client, code, err := websocket.DefaultDialer.Dial(wssEndpoint, nil)
	if err != nil {
		// Error with the code and error
		msg := fmt.Sprintf("failed to connect to the WebSocket server: %d %v", code.StatusCode, err)
		log.Println(msg)
		return fmt.Errorf("error: %s", msg)
	}

	clients[connectionID] = &WebSocketClient{
		Conn: client,
		ID:   connectionID,
	}

	// Start a goroutine to read messages from the WebSocket server and just print them for now
	go eventListener(connectionID, client)

	return nil
}

func connectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST requests are allowed", http.StatusMethodNotAllowed)
		return
	}

	var data SimpleContext
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}
	defer r.Body.Close()

	// Create a new WebSocket client and add it to the clients map using the connection ID as the key
	err := connectToOpenAI(data.ConnectionID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Failed to connect: %v", err)})
		return
	}

	w.WriteHeader(http.StatusOK)
}

func disconnectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Only DELETE requests are allowed", http.StatusMethodNotAllowed)
		return
	}

	var data SimpleContext
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}
	defer r.Body.Close()

	// Close the WebSocket connection and remove the client from the clients map
	clientsMu.Lock()
	defer clientsMu.Unlock()
	client, ok := clients[data.ConnectionID]
	if ok {
		client.Conn.Close()
		delete(clients, data.ConnectionID)
	}

	w.WriteHeader(http.StatusOK)
}

func forwardMessageToOpenAI(connectionID string, message []byte) error {
	// read only operation, no need to lock
	client, ok := clients[connectionID]
	if !ok {
		return fmt.Errorf("client with connection ID %s not found", connectionID)
	}

	err := client.Conn.WriteMessage(websocket.TextMessage, message)
	if err != nil {
		return fmt.Errorf("failed to send message to OpenAI: %v", err)
	}

	return nil
}

func defaultHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST requests are allowed", http.StatusMethodNotAllowed)
		return
	}

	var data MessageContext
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}
	defer r.Body.Close()

	// Extract connectionId from data
	connectionID := data.ConnectionId
	message := fmt.Appendf(nil, "msg recieved: %s", data.Body.Message)

	// Forward the message to the OpenAI WebSocket server
	err := forwardMessageToOpenAI(connectionID, message)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Failed to send message: %v", err)})
		return
	}

	w.WriteHeader(http.StatusOK)
}

func sendMessageToClient(connectionID string, message []byte) error {
	ctx := context.TODO()

	input := &apigatewaymanagementapi.PostToConnectionInput{
		ConnectionId: aws.String(connectionID),
		Data:         message,
	}

	_, err := apiGatewayClient.PostToConnection(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to send message: %v", err)
	}
	return nil
}

func healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET requests are allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/connect", connectHandler)
	mux.HandleFunc("/disconnect", disconnectHandler)
	mux.HandleFunc("/msg", defaultHandler)
	mux.HandleFunc("/health", healthCheckHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:    fmt.Sprintf("0.0.0.0:%s", port),
		Handler: mux,
	}

	log.Println("Server is running on port 8080...")
	log.Fatal(server.ListenAndServe())
}

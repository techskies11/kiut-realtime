package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
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

func connectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST requests are allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	var data SimpleContext
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}
	defer r.Body.Close()

	response := fmt.Sprintf("Connecting: %s", data)
	println(response)

	w.WriteHeader(http.StatusOK)
}

func disconnectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Only DELETE requests are allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	var data SimpleContext
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}
	defer r.Body.Close()

	response := fmt.Sprintf("Disconnecting: %s", data)
	println(response)

	w.WriteHeader(http.StatusOK)
}

func sendMessageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST requests are allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	var data MessageContext
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}
	defer r.Body.Close()

	response := fmt.Sprintf("I got your message!: %s", data)
	println(response)

	// Extract connectionId from data
	connectionID := data.ConnectionId
	message := []byte(data.Body.Message)
	if (sendMessageToClient(connectionID, message)) != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to send message"})
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
		println(err)
		return fmt.Errorf("failed to send message: %w", err)
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
	mux.HandleFunc("/sendMessage", sendMessageHandler)
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

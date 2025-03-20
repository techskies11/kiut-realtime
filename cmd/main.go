package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

type SessionTranscription struct {
	Model string `json:"model"`
}

type OpenAISession struct {
	Instructions            string                `json:"instructions"`
	InputAudioFormat        string                `json:"input_audio_format"`
	OutputAudioFormat       string                `json:"output_audio_format"`
	InputAudioTranscription *SessionTranscription `json:"input_audio_transcription"`
}

type SessionUpdate struct {
	Type    string        `json:"type"`
	Session OpenAISession `json:"session"`
}

type GenericEvent struct {
	Type string `json:"type"`
}

type AudioEvent struct {
	Type  string `json:"type"`
	Audio string `json:"audio"`
}

type AudioMessage struct {
	Body         AudioEvent `json:"body"`
	ConnectionId string     `json:"connectionId"`
}

type AudioDeltaEvent struct {
	Delta string `json:"delta"`
}

type TwilioMediaInfo struct {
	Track     string `json:"track"`
	Chunk     string `json:"chunk"`
	Timestamp string `json:"timestamp"`
	Payload   string `json:"payload"`
}

type TwilioMediaEvent struct {
	Event     string          `json:"event"`
	StreamSID string          `json:"streamSid"`
	Media     TwilioMediaInfo `json:"media"`
}

type BaseTwilioEvent struct {
	Event     string `json:"event"`
	StreamSID string `json:"streamSid"`
}

type TwilioMediaGatewayEvent struct {
	Body         TwilioMediaEvent `json:"body"`
	ConnectionID string           `json:"connectionId"`
}

type TwilioGatewayEvent struct {
	Body         BaseTwilioEvent `json:"body"` // for now we're stuck with twilio events
	ConnectionID string          `json:"connectionId"`
}

var (
	clients   = make(map[string]*websocket.Conn)
	clientsMu sync.Mutex
)

var (
	connectionToStreamSID = make(map[string]string)
	streamSIDMu           sync.Mutex
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

func setupServerConfigs(client *websocket.Conn) error {
	// initialize server configs. send a session.update event type to the client
	// to update the session state of the form {"type": "session.update", "data": {"state": "init"}}
	log.Println("Setting up openai server configs...")

	sessionUpdate := SessionUpdate{
		Type: "session.update",
		Session: OpenAISession{
			Instructions:            "Eres un asistente de ventas para una aerolínea. Eres tajante y conciso. Te restringes únicamente a responder sus preguntas asociadas a sus viajes, o lo guías a ese tipo de conversación.",
			InputAudioFormat:        "g711_ulaw",
			OutputAudioFormat:       "g711_ulaw",
			InputAudioTranscription: nil,
		},
	}
	client.WriteJSON(sessionUpdate)

	return nil
}

func createTwilioMediaEvent(streamSID string, audioDelta AudioDeltaEvent) TwilioMediaEvent {
	return TwilioMediaEvent{
		Event:     "media",
		StreamSID: streamSID,
		Media: TwilioMediaInfo{
			Payload: audioDelta.Delta,
		},
	}
}

func handleEvent(connectionID string, message []byte) {
	var event GenericEvent
	err := json.Unmarshal(message, &event)
	if err != nil {
		log.Printf("[Listener] Error unmarshalling message for connection %s: %v", connectionID, err)
		return
	} else {
		log.Printf("[Listener] Event listened: %s", event.Type)
	}
	log.Printf("[Listener] Handling event of type: %s for connection %s", event.Type, connectionID)

	if event.Type != "response.audio.delta" {
		return
	}

	var audioDelta AudioDeltaEvent
	err = json.Unmarshal(message, &audioDelta)
	if err != nil {
		log.Printf("[Listener] Error unmarshalling audio delta for connection %s: %v", connectionID, err)
		return
	}

	streamSIDMu.Lock()
	streamSID, ok := connectionToStreamSID[connectionID]
	streamSIDMu.Unlock()
	if ok {
		twilioMediaEvent := createTwilioMediaEvent(streamSID, audioDelta)
		message, err := json.Marshal(twilioMediaEvent)
		if err != nil {
			log.Printf("[Listener] Error marshalling Twilio Media Event for some reason: %v", err)
			return
		}
		err = sendMessageToClient(connectionID, message)
		if err != nil {
			log.Printf("[Listener] Error sending message to client %s: %v", connectionID, err)
			return
		}
	}
}

func eventListener(connectionID string, client *websocket.Conn) {
	// Listen for messages from the WebSocket server and send them to the client
	log.Printf("[Listener] Started listening for connection: %s", connectionID)

	for {
		msgType, message, err := client.ReadMessage()
		if err != nil {
			log.Printf("[Listener] Error reading message for connection %s: %v", connectionID, err)
			break
		}
		log.Printf("[Listener] Received message from OpenAI for connection type:%d, %s", msgType, connectionID)
		handleEvent(connectionID, message)
	}
}

func connectToOpenAI(connectionID string) error {
	// Connect to the OpenAI WebSocket server
	log.Println("Connecting to OpenAI WebSocket server...")

	wssEndpoint := os.Getenv("OPENAI_WSS_URL")
	OPENAI_API_KEY := os.Getenv("OPENAI_API_KEY")
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+OPENAI_API_KEY)
	headers.Set("OpenAI-Beta", "realtime=v1")

	client, code, err := websocket.DefaultDialer.Dial(wssEndpoint, headers)
	if err != nil {
		// Error with the code and error
		msg := fmt.Sprintf("failed to connect to the WebSocket server: %d %v", code.StatusCode, err)
		log.Println(msg)
		return fmt.Errorf("error: %s", msg)
	}

	clientsMu.Lock()
	clients[connectionID] = client
	clientsMu.Unlock()

	// Setup server configs
	err = setupServerConfigs(client)
	if err != nil {
		log.Printf("error: %v", err)
		return fmt.Errorf("error: %v", err)
	}
	// Start a goroutine to read messages from the WebSocket server and just print them for now
	go eventListener(connectionID, client)

	log.Println("Successfully connected to OpenAI WebSocket server")

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

	log.Printf("[AWS] Successfully connected client... %s", data.ConnectionID)

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

	log.Printf("[AWS] Disconnecting client... %s", data.ConnectionID)

	// Close the WebSocket connection and remove the client from the clients map
	clientsMu.Lock()
	defer clientsMu.Unlock()
	client, ok := clients[data.ConnectionID]
	if ok {
		client.Close()
		delete(clients, data.ConnectionID)
	}
	streamSIDMu.Lock()
	defer streamSIDMu.Unlock()
	_, ok = connectionToStreamSID[data.ConnectionID]
	if ok {
		delete(connectionToStreamSID, data.ConnectionID)
	}

	w.WriteHeader(http.StatusOK)
}

func forwardMessageToOpenAI(connectionID string, event AudioEvent) error {
	log.Printf("[OpenAI] forwarding message to OpenAI: %s", event.Type)
	// read only lock
	clientsMu.Lock()
	client, ok := clients[connectionID]
	clientsMu.Unlock()
	if !ok {
		return fmt.Errorf("[OpenAI] client with connection ID %s not found", connectionID)
	}

	// forward the message to the OpenAI WebSocket server. sends both type and audio from AudioEvent
	log.Print("[OpenAI] sending message to OpenAI")
	err := client.WriteJSON(event)
	if err != nil {
		log.Printf("[OpenAI] failed to send message to OpenAI: %v", err)
		return fmt.Errorf("[OpenAI] failed to send message to OpenAI: %v", err)
	}

	log.Printf("[OpenAI] Successfully forwarded message to OpenAI")
	return nil
}

func twilioToOpenAIEvent(event TwilioMediaEvent) AudioEvent {
	// convert the TwilioMediaEvent to an AudioMessage
	return AudioEvent{
		Type:  "input_audio_buffer.append",
		Audio: event.Media.Payload,
	}
}

func processMediaEvent(connectionID string, event TwilioMediaEvent) error {
	// Convert the Twilio media event to an AudioMessage
	openaiEvent := twilioToOpenAIEvent(event)

	// Forward the message to the OpenAI WebSocket server
	err := forwardMessageToOpenAI(connectionID, openaiEvent)
	if err != nil {
		log.Printf("[TWILIO] Error forwarding message to OpenAI: %v", err)
		return err
	}
	return nil
}

func defaultHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST requests are allowed", http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		log.Printf("[TWILIO] Error reading request body: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Error reading request body"})
		return
	}

	var data TwilioGatewayEvent
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		log.Printf("[TWILIO] Invalid JSON: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}

	connectionID := data.ConnectionID
	mediaEventBody := data.Body
	log.Printf("[TWILIO] Received media event: %s", mediaEventBody.Event)

	if mediaEventBody.Event == "start" {
		streamSIDMu.Lock()
		connectionToStreamSID[connectionID] = mediaEventBody.StreamSID
		streamSIDMu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	}
	if mediaEventBody.Event != "media" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Convert the media event to a TwilioMediaEvent
	var mediaEvent TwilioMediaGatewayEvent
	if err := json.Unmarshal(bodyBytes, &mediaEvent); err != nil {
		log.Printf("[TWILIO] Invalid media type: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}

	if err := processMediaEvent(connectionID, mediaEvent.Body); err != nil {
		log.Printf("[TWILIO] Error processing media event: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Failed to process media event: %v", err)})
		return
	}

	w.WriteHeader(http.StatusOK)
}

func sendMessageToClient(connectionID string, message []byte) error {
	log.Printf("[AWS] Sending message to API Gateway WebSocket (connection=%s)", connectionID)
	ctx := context.TODO()

	input := &apigatewaymanagementapi.PostToConnectionInput{
		ConnectionId: aws.String(connectionID),
		Data:         message,
	}

	_, err := apiGatewayClient.PostToConnection(ctx, input)
	if err != nil {
		log.Printf("[AWS] Failed to send message to client %s: %v", connectionID, err)
		return fmt.Errorf("failed to send message to client %s: %v", connectionID, err)
	}
	log.Printf("[AWS] Successfully sent message to client %s", connectionID)
	return nil
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/connect", connectHandler)
	mux.HandleFunc("/disconnect", disconnectHandler)
	mux.HandleFunc("/msg", defaultHandler)

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

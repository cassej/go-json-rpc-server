package main

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"os"
	"plugin"
	"strings"
	"sync"
)

var ErrorLog = log.New(os.Stderr, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)

type JSONRPCRequest struct {
	Method string                 `json:"method"`
	Params map[string]interface{} `json:"params"`
	Id     *int                   `json:"id"`
}

type JSONRPCResponse struct {
	Result interface{} `json:"result,omitempty"`
	Error  *RPCError   `json:"error,omitempty"`
	Id     *int        `json:"id"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	listener, err := net.Listen("tcp", ":8080")
	if err != nil {
		panic(err)
	}
	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			ErrorLog.Println(err)
			continue
		}

		go handler(conn)
	}
}

func handler(conn net.Conn) {
	reader := bufio.NewReader(conn)
	defer conn.Close()

	for {
		message, err := reader.ReadString('\n')
		if err != nil {
			ErrorLog.Println(err)
			return
		}

		// Определение, является ли запрос batch-запросом
		var data json.RawMessage
		if err := json.Unmarshal([]byte(message), &data); err != nil {
			sendErrorResponse(conn, -32700, "Parse error", nil)
			continue
		}

		// Попытка десериализации batch-запроса
		var batch []JSONRPCRequest
		if err := json.Unmarshal(data, &batch); err == nil {
			handleBatchRequest(conn, batch)
			continue
		}

		// Попытка десериализации одиночного запроса
		var singleRequest JSONRPCRequest
		if err := json.Unmarshal(data, &singleRequest); err == nil {
			handleSingleRequest(conn, singleRequest)
			continue
		}

		sendErrorResponse(conn, -32600, "Invalid Request", nil)
	}
}

func handleBatchRequest(conn net.Conn, requests []JSONRPCRequest) {
	var wg sync.WaitGroup
	responses := make([]JSONRPCResponse, len(requests))

	for i, req := range requests {
		wg.Add(1)
		go func(index int, request JSONRPCRequest) {
			defer wg.Done()
			response := callMethod(request.Method, &request.Params)
			response.Id = request.Id
			responses[index] = response
		}(i, req)
	}

	wg.Wait()

	// Отфильтровываем пустые ответы для уведомлений (notifications)
	var nonEmptyResponses []JSONRPCResponse
	for _, resp := range responses {
		if resp.Id != nil { // Уведомления не имеют id
			nonEmptyResponses = append(nonEmptyResponses, resp)
		}
	}

	responseJSON, err := json.Marshal(nonEmptyResponses)
	if err != nil {
		ErrorLog.Println("Ошибка при маршалинге ответа:", err)
		return
	}

	conn.Write(responseJSON)
	conn.Write([]byte("\n"))
}

func handleSingleRequest(conn net.Conn, request JSONRPCRequest) {
	response := callMethod(request.Method, &request.Params)
	response.Id = request.Id
	responseJSON, err := json.Marshal([]JSONRPCResponse{response}) // Оборачиваем в массив для соответствия JSON-RPC
	if err != nil {
		ErrorLog.Println("Ошибка при маршалинге ответа:", err)
		return
	}

	conn.Write(responseJSON)
	conn.Write([]byte("\n"))
}

func callMethod(method string, params *map[string]interface{}) JSONRPCResponse {
	path := "./methods/" + strings.Replace(method, ".", "/", -1) + ".so"

	p, err := plugin.Open(path)
	if err != nil {
		return JSONRPCResponse{Error: &RPCError{-32601, "Method not found"}}
	}

	symbol, err := p.Lookup("Do")
	if err != nil {
		ErrorLog.Println(err)
		return JSONRPCResponse{Error: &RPCError{-32601, "Method not found"}}
	}

	exportedFunc, ok := symbol.(func(map[string]interface{}) interface{})
	if !ok {
		ErrorLog.Println("Method", method, "is broken or has incorrect signature")
		return JSONRPCResponse{Error: &RPCError{-32601, "Method not found"}}
	}

	result := exportedFunc(*params)
	return JSONRPCResponse{Result: result}
}

func sendErrorResponse(conn net.Conn, code int, message string, id *int) {
	resp := JSONRPCResponse{
		Error: &RPCError{
			Code:    code,
			Message: message,
		},
		Id: id,
	}
	respJSON, _ := json.Marshal(resp)
	conn.Write(respJSON)
	conn.Write([]byte("\n"))
}

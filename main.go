package main

import (
    "context"
    "encoding/json"
    "expvar"
    "flag"
    "fmt"
    "log"
    "net/http"
    "os"
    "os/signal"
    "runtime"
    "sync"
    "syscall"
    "time"

    "github.com/vmihailenco/msgpack/v5"
)

var ErrorLog = log.New(os.Stderr, "ERROR: ", log.Ldate|log.Ltime)
var NoticeLog = log.New(os.Stderr, "NOTICE: ", log.Ldate|log.Ltime)
var configPath = flag.String("config", "config.json", "path to config file")
var config *Config
var wg sync.WaitGroup

var (
    requestCount = expvar.NewInt("requestCount")
    errorCount   = expvar.NewInt("errorCount")
)

func init() {
    expvar.Publish("goroutines", expvar.Func(func() interface{} {
        return runtime.NumGoroutine()
    }))
}

type Serializer interface {
    Marshal(v interface{}) ([]byte, error)
    Unmarshal(data []byte, v interface{}) error
}

type JSONSerializer struct{}

func (js JSONSerializer) Marshal(v interface{}) ([]byte, error) {
    return json.Marshal(v)
}

func (js JSONSerializer) Unmarshal(data []byte, v interface{}) error {
    return json.Unmarshal(data, v)
}

type MsgPackSerializer struct{}

func (mps MsgPackSerializer) Marshal(v interface{}) ([]byte, error) {
    return msgpack.Marshal(v)
}

func (mps MsgPackSerializer) Unmarshal(data []byte, v interface{}) error {
    return msgpack.Unmarshal(data, v)
}

func main() {
    flag.Parse()
    config = LoadConfig(*configPath)

    if config == nil {
        ErrorLog.Fatal("Failed to load configuration")
    }

    rpc.init()

    sighupChan := make(chan os.Signal, 1)
    stopChan := make(chan os.Signal, 1)
    signal.Notify(sighupChan, syscall.SIGHUP)
    signal.Notify(stopChan, syscall.SIGINT, syscall.SIGTERM)

    requestQueue = make(chan Request, config.Server.MaxRequests)

    // Создаем контекст с возможностью отмены
    ctx, cancel := context.WithCancel(context.Background())

    // Запускаем серверы
    var httpServer *http.Server
    if config.Server.Protocol == "tcp" {
        go TCPServer(ctx)
    } else if config.Server.Protocol == "http" {
        httpServer = HTTPServer()
        go func() {
            if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
                ErrorLog.Printf("HTTP server error: %v", err)
            }
        }()
    }

    // Запускаем сервер метрик
    metricsServer := &http.Server{Addr: ":8080"}
    go func() {
        if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            ErrorLog.Printf("Metrics server error: %v", err)
        }
    }()

    // Создаем пул воркеров
    workerPool := make(chan struct{}, config.Server.MaxWorkers)
    for i := 0; i < config.Server.MaxWorkers; i++ {
        workerPool <- struct{}{}
    }

    // Запускаем обработку запросов
    go func() {
        for {
            select {
            case req := <-requestQueue:
                <-workerPool // Получаем доступный воркер
                go func(req Request) {
                    defer func() {
                        workerPool <- struct{}{} // Возвращаем воркер в пул
                    }()
                    handleRequest(req)
                }(req)
            case <-ctx.Done():
                return
            }
        }
    }()

    // Ожидаем сигналы
    for {
        select {
        case <-sighupChan:
            NoticeLog.Println("Received SIGHUP, reinitializing methods.")
            rpc.init()
        case <-stopChan:
            NoticeLog.Println("Received stop signal, shutting down...")

            // Отменяем контекст, чтобы остановить обработку новых запросов
            cancel()

            // Закрываем каналы
            close(requestQueue)
            close(workerPool)

            // Ожидаем завершения текущих запросов (можно добавить таймаут)
            time.Sleep(5 * time.Second)

            // Останавливаем HTTP-сервер, если он запущен
            if httpServer != nil {
                shutdownCtx, _ := context.WithTimeout(context.Background(), 10*time.Second)
                if err := httpServer.Shutdown(shutdownCtx); err != nil {
                    ErrorLog.Printf("HTTP server shutdown error: %v", err)
                }
            }

            // Останавливаем сервер метрик
            shutdownCtx, _ := context.WithTimeout(context.Background(), 5*time.Second)
            if err := metricsServer.Shutdown(shutdownCtx); err != nil {
                ErrorLog.Printf("Metrics server shutdown error: %v", err)
            }

            NoticeLog.Println("Shutdown complete")
            os.Exit(0)
        }
    }
}

func handleRequest(req Request) {
    requestCount.Add(1)
    var response []byte
    var serializer Serializer

    switch config.Server.Format {
    case "JSON":
        serializer = JSONSerializer{}
    case "MessagePack":
        serializer = MsgPackSerializer{}
    }

    raw, err := deserializeRequest(serializer, []byte(req.Data))
    if err != nil {
        ErrorLog.Printf("%s", err)
        errorCount.Add(1)
        response, _ = serializeResponse(serializer, RPCResponse{Error: &RPCError{-32700, "Parse error"}})
    } else if err := validateRPCRequest(raw); err != nil {
        ErrorLog.Printf("Invalid request: %s", err)
        errorCount.Add(1)
        response, _ = serializeResponse(serializer, RPCResponse{Error: &RPCError{-32600, "Invalid Request"}})
    } else {
        switch raw.(type) {
        case []interface{}:
            // Обрабатываем как батч запрос
            var requests []RPCRequest
            if err := serializer.Unmarshal([]byte(req.Data), &requests); err != nil {
                response, _ = serializeResponse(serializer, RPCResponse{Error: &RPCError{-32600, "Invalid Batch Request"}})
            }

            responses := make([]RPCResponse, len(requests))

            for i, subreq := range requests {
                if len(req.Token) > 0 && len(subreq.Token) == 0 {
                    subreq.Token = req.Token
                }
                responses[i] = *rpc.handle(subreq)
            }

            response, _ = serializeResponse(serializer, responses)

        case map[string]interface{}:
            // Обрабатываем как одиночный запрос
            var request RPCRequest
            if err := serializer.Unmarshal([]byte(req.Data), &request); err != nil {
                response, _ = serializeResponse(serializer, RPCResponse{Error: &RPCError{-32600, "Invalid Request"}})
            } else {
                if len(req.Token) > 0 && len(request.Token) == 0 {
                    request.Token = req.Token
                }
                response, _ = serializeResponse(serializer, rpc.handle(request))
            }
        }
    }

    req.Response(string(response))
}

func validateRPCRequest(raw interface{}) error {
    switch v := raw.(type) {
    case []interface{}:
        for _, item := range v {
            if err := validateRPCRequestItem(item); err != nil {
                return err
            }
        }
    case map[string]interface{}:
        return validateRPCRequestItem(v)
    default:
        return fmt.Errorf("invalid request format")
    }
    return nil
}

func validateRPCRequestItem(item interface{}) error {
    req, ok := item.(map[string]interface{})
    if !ok {
        return fmt.Errorf("invalid request item format")
    }
    if _, ok := req["method"]; !ok {
        return fmt.Errorf("method is required")
    }
    if _, ok := req["params"]; !ok {
        return fmt.Errorf("params are required")
    }
    return nil
}

func serializeResponse(serializer Serializer, resp interface{}) ([]byte, error) {
    return serializer.Marshal(resp)
}

func deserializeRequest(serializer Serializer, data []byte) (interface{}, error) {
    var raw interface{}
    err := serializer.Unmarshal(data, &raw)
    return raw, err
}
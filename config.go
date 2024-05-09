package main

import (
	"encoding/json"
	"io/ioutil"
	"os"
)

// Config структура, которая соответствует структуре нашего JSON файла.
type Config struct {
	Server struct {
		Protocol   string `json:"protocol"`
		Format     string `json:"format"`
		Host       string `json:"host"`
		Port       string  `json:"port"`
		MaxWorkers int    `json:"max_workers"`
		MaxRequests int    `json:"max_requests"`
	}
}

func LoadConfig(filename string) (*Config) {
	file, err := os.Open(filename)
	if err != nil {
	    ErrorLog.Fatal(err)
		return nil
	}
	defer file.Close()

	bytes, err := ioutil.ReadAll(file)
	if err != nil {
	    ErrorLog.Fatal(err)
		return nil
	}

	var config Config
	err = json.Unmarshal(bytes, &config)
	if err != nil {
	    ErrorLog.Fatal(err)
		return nil
	}

	return &config
}
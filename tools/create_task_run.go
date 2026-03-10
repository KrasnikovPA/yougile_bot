// Command create_task_run is a small helper that creates a test task in Yougile.
// It is intended for local debugging and manual runs.
package main

import (
	"fmt"
	"os"
	"time"

	"yougile_bot4/internal/api"
	"yougile_bot4/internal/models"
)

func main() {
	token := os.Getenv("YOUGILE_TOKEN")
	board := os.Getenv("YOUGILE_BOARD")
	if token == "" || board == "" {
		fmt.Println("missing YOUGILE_TOKEN or YOUGILE_BOARD in environment")
		os.Exit(2)
	}

	client := api.NewClient(token, board, 30*time.Second, nil)

	// optional COLUMN_ID environment variable
	column := os.Getenv("COLUMN_ID")
	// assigned env intentionally ignored

	t := &models.Task{
		Title:       "Тест",
		Description: "Тест из VSCode",
	}
	if column != "" {
		t.ColumnID = column
	}
	// ASSIGNED env is ignored for task creation per request

	if err := client.CreateTask(t); err != nil {
		fmt.Printf("CreateTask failed: %v\n", err)
		os.Exit(1)
	}
	if t.ExternalID != "" {
		fmt.Printf("CreateTask OK, new task ExternalID: %s\n", t.ExternalID)
	} else {
		fmt.Printf("CreateTask OK, new task ID: %d\n", t.ID)
	}
}

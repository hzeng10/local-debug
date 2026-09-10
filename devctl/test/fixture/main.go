// A deterministic fake Kubernetes/tool environment for end-to-end lifecycle tests.
// Never contacts a real cluster or alters network interfaces.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	args := os.Args[1:]
	base := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	switch base {
	case "kubectl":
		if strings.Contains(strings.Join(args, " "), "get deployment") {
			fmt.Print(`{"spec":{"template":{"spec":{"containers":[{"name":"app","envFrom":[{"secretRef":{"name":"creds"}}],"env":[{"name":"MESSAGE","value":"shared-config"}]}]}}}}`)
			return
		}
		if strings.Contains(strings.Join(args, " "), "get secret") {
			fmt.Print(`{"data":{"PASSWORD":"dGVzdC1zZWNyZXQtMTIz"}}`)
			return
		}
		os.Exit(1)
	case "telepresence":
		path := ""
		for i, a := range args {
			if a == "--config" {
				path = filepath.Join(filepath.Dir(args[i+1]), "fake-connected")
			}
		}
		for _, a := range args {
			switch a {
			case "connect":
				_ = os.WriteFile(path, []byte("yes"), 0600)
				fmt.Print(`{}`)
				return
			case "quit":
				if _, e := os.Stat(filepath.Join(filepath.Dir(path), "fail-quit")); e == nil {
					os.Exit(9)
				}
				_ = os.Remove(path)
				return
			case "status":
				if _, e := os.Stat(path); e == nil {
					fmt.Print(`{"user_daemon":{"status":"Connected"}}`)
				} else {
					fmt.Print(`{"user_daemon":{"status":"Disconnected"}}`)
				}
				return
			}
		}
		os.Exit(1)
	case "java":
		fmt.Fprintln(os.Stderr, `openjdk version "21.0.1"`)
		return
	}
	if len(args) < 2 {
		os.Exit(2)
	}
	switch args[0] {
	case "app":
		fmt.Println("PASSWORD=" + os.Getenv("PASSWORD"))
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
		http.HandleFunc("/check", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"message": os.Getenv("MESSAGE"), "password": os.Getenv("PASSWORD"), "ambient": os.Getenv("SPRING_APPLICATION_JSON")})
		})
		if e := http.ListenAndServe(args[1], nil); e != nil {
			os.Exit(3)
		}
	case "assert":
		resp, e := http.Get(args[1] + "/check")
		if e != nil {
			os.Exit(4)
		}
		defer resp.Body.Close()
		var data map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&data)
		if data["message"] != "local-override" || data["password"] != "test-secret-123" || data["ambient"] != "" {
			os.Exit(5)
		}
	case "fail":
		os.Exit(7)
	}
}

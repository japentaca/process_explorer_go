package main

import (
	"fmt"
	"os"
	"time"
	"encoding/json"
	"net/http"
	"bytes"
	"log"
	"github.com/shirou/gopsutil/v3/process"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
	"github.com/joho/godotenv"
)

type ProcessInfo struct {
	PID          int    `json:"pid"`
	Name         string `json:"name"`
	Executable   string `json:"executablePath"`
	MemoryUsage  int    `json:"memoryUsage"`
	ThreadCount  int    `json:"threadCount"`
	HandleCount  int    `json:"handleCount"`
	StartTime    string `json:"startTime"`
	CommandLine  string `json:"commandLine"`
	UserName     string `json:"userName"`
	CPUUsage     float64 `json:"cpuUsage"`
}

const serviceName = "ProcessExplorerService"

type myService struct{}

func (m *myService) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}

	go func() {
		main() // Call the main function to start the process monitoring logic
	}()

	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				return false, 0
			}
		}
	}
}

func installService() error {
	m, err := mgr.Connect()
	if (err != nil) {
		return err
	}
	defer m.Disconnect()

	s, err := m.CreateService(serviceName, os.Args[0], mgr.Config{DisplayName: "Process Explorer Service"})
	if err != nil {
		return err
	}
	defer s.Close()

	return eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info)
}

func removeService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer s.Close()

	return s.Delete()
}

func runService() error {
	return svc.Run(serviceName, &myService{})
}

func getProcesses() []ProcessInfo {
	processes, err := process.Processes()
	if err != nil {
		log.Printf("Failed to fetch processes: %v", err)
		return nil
	}

	var processInfoList []ProcessInfo
	for _, p := range processes {
		name := "Unknown"
		if n, err := p.Name(); err == nil {
			name = n
		} else {
			//log.Printf("Failed to get name for PID %d: %v", p.Pid, err)
		}

		exe := "Unknown"
		if e, err := p.Exe(); err == nil {
			exe = e
		} else {
			//log.Printf("Failed to get executable path for PID %d: %v", p.Pid, err)
		}

		var memUsage int
		if memInfo, err := p.MemoryInfo(); err == nil {
			memUsage = int(memInfo.RSS)
		} else {
		//	log.Printf("Failed to get memory info for PID %d: %v", p.Pid, err)
		}

		threads := 0
		if t, err := p.NumThreads(); err == nil {
			threads = int(t)
		} else {
			//log.Printf("Failed to get thread count for PID %d: %v", p.Pid, err)
		}

		handles := 0
		if h, err := p.NumFDs(); err == nil {
			handles = int(h)
		} else {
			//log.Printf("Failed to get handle count for PID %d: %v", p.Pid, err)
		}

		startTime := "Unknown"
		if createTime, err := p.CreateTime(); err == nil {
			startTime = time.Unix(createTime/1000, 0).Format(time.RFC3339)
		} else {
			//log.Printf("Failed to get start time for PID %d: %v", p.Pid, err)
		}

		cmdline := "Unknown"
		if c, err := p.Cmdline(); err == nil {
			cmdline = c
		} else {
			//log.Printf("Failed to get command line for PID %d: %v", p.Pid, err)
		}

		username := "Unknown"
		if u, err := p.Username(); err == nil {
			username = u
		} else {
			//log.Printf("Failed to get username for PID %d: %v", p.Pid, err)
		}

		cpuUsage := 0.0
		if cpu, err := p.CPUPercent(); err == nil {
			cpuUsage = cpu
		} else {
			//log.Printf("Failed to get CPU usage for PID %d: %v", p.Pid, err)
		}

		if cpuUsage < 1.0 {
			continue
		}

		processInfoList = append(processInfoList, ProcessInfo{
			PID:         int(p.Pid),
			Name:        name,
			Executable:  exe,
			MemoryUsage: memUsage,
			ThreadCount: int(threads),
			HandleCount: int(handles),
			StartTime:   startTime,
			CommandLine: cmdline,
			UserName:    username,
			CPUUsage:    cpuUsage,
		})
	}

	return processInfoList
}

func sendDataToServer(data []ProcessInfo, url string) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to send data: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server returned non-success status: %d", resp.StatusCode)
	}

	return nil
}

func loadEnv() string {
	if err := godotenv.Load(".env"); err != nil {
		log.Printf("Error loading .env file: %v", err)
		return ""
	}
	return os.Getenv("SERVER_URL")
}

func main() {
	logFile, err := os.OpenFile("process_monitor.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("Failed to open log file: %v\n", err)
		return
	}
	defer logFile.Close()

	logger := log.New(logFile, "", log.LstdFlags)
	logger.Println("Process Monitor starting.")
	logger.Println("Debug: Logging system initialized.")

	serverURL := loadEnv()
	if serverURL == "" {
		logger.Println("Server URL not found in environment variables.")
		return
	}

	interval := 20 * time.Second

	for {
		processes := getProcesses()
		logger.Printf("Collected %d processes.\n", len(processes))

		if err := sendDataToServer(processes, serverURL); err != nil {
			logger.Printf("Failed to send data: %v\n", err)
		}

		time.Sleep(interval)
	}
}
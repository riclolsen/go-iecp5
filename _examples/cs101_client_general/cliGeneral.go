package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs101"
	"go.bug.st/serial"
)

// MyClientHandler implements the cs101.ClientHandlerInterface
type MyClientHandler struct{}

// InterrogationHandler handles responses to interrogation commands.
func (h *MyClientHandler) InterrogationHandler(asduPack *asdu.ASDU) error {
	fmt.Printf("HANDLER: Received Interrogation Response: %s\n", asduPack.Identifier)
	// Process the ASDU data here if needed
	return nil
}

// CounterInterrogationHandler handles responses to counter interrogation commands.
func (h *MyClientHandler) CounterInterrogationHandler(asduPack *asdu.ASDU) error {
	fmt.Printf("HANDLER: Received Counter Interrogation Response: %s\n", asduPack.Identifier)
	return nil
}

// ReadHandler handles responses to read commands.
func (h *MyClientHandler) ReadHandler(asduPack *asdu.ASDU) error {
	fmt.Printf("HANDLER: Received Read Response: %s\n", asduPack.Identifier)
	return nil
}

// TestCommandHandler handles responses to test commands.
func (h *MyClientHandler) TestCommandHandler(asduPack *asdu.ASDU) error {
	fmt.Printf("HANDLER: Received Test Command Response: %s\n", asduPack.Identifier)
	return nil
}

// ClockSyncHandler handles responses related to clock synchronization.
func (h *MyClientHandler) ClockSyncHandler(asduPack *asdu.ASDU) error {
	fmt.Printf("HANDLER: Received Clock Sync Response/Ack: %s\n", asduPack.Identifier)
	return nil
}

// ResetProcessHandler handles responses related to reset process commands.
func (h *MyClientHandler) ResetProcessHandler(asduPack *asdu.ASDU) error {
	fmt.Printf("HANDLER: Received Reset Process Response/Ack: %s\n", asduPack.Identifier)
	return nil
}

// DelayAcquisitionHandler handles responses related to delay acquisition commands.
func (h *MyClientHandler) DelayAcquisitionHandler(asduPack *asdu.ASDU) error {
	fmt.Printf("HANDLER: Received Delay Acquisition Response/Ack: %s\n", asduPack.Identifier)
	return nil
}

// ASDUHandler handles ASDUs not caught by specific handlers.
func (h *MyClientHandler) ASDUHandler(asduPack *asdu.ASDU, clientNum int) error {
	fmt.Printf("HANDLER: Received Generic ASDU: %s\n", asduPack.Identifier)
	return nil
}

// ASDUHandlerAll handles all received ASDUs before specific handlers.
func (h *MyClientHandler) ASDUHandlerAll(asduPack *asdu.ASDU, clientNum int) error {
	fmt.Printf("HANDLER (ALL): Received ASDU: %s\n", asduPack.Identifier)
	return nil
}

func main() {
	// --- Configuration ---
	// !! IMPORTANT: Change this to your actual serial port !!
	serialPortAddress := "COM3" // e.g., "COM3" on Windows

	serialCfg := cs101.SerialConfig{
		Address:  serialPortAddress,
		BaudRate: 9600,
		DataBits: 8,
		StopBits: serial.OneStopBit,
		Parity:   serial.EvenParity, // Common for IEC 101
		Timeout:  5 * time.Second,   // Read timeout
	}

	// Client options using default CS101 parameters
	option := cs101.NewOption()
	params := asdu.ParamsStandard101
	params.CommonAddrSize = 1
	params.CauseSize = 1
	params.InfoObjAddrSize = 2
	params.OrigAddress = 1
	option.SetParams(params)
	option.SetAutoReconnect(false) // disable auto reconnect (we will handle it)

	cfg := cs101.DefaultConfig()
	cfg.Serial = serialCfg
	cfg.LinkAddress = 1 // Address of the secondary station we want to talk to
	cfg.TimeoutSendLinkMsg = 50 * time.Millisecond
	option.SetConfig(cfg)
	option.SetSerialConfig(serialCfg)

	handler := &MyClientHandler{}
	client := cs101.NewClient(handler, option)
	if client == nil {
		log.Fatalf("Failed to create client. Check serial port configuration.")
	}

	// --- Callbacks (Optional) ---
	client.SetOnConnectHandler(func(c *cs101.Client) {
		fmt.Println("CLIENT: Successfully connected!")
		// Send commands after connection established and link is reset/active
		go func() {
			for i := 0; i < 5; i++ {
				time.Sleep(1000 * time.Millisecond)
				// c.SendReqDataClass2() // Request class 2 data
			}

			fmt.Println("CLIENT: Sending Interrogation Command...")
			err := c.InterrogationCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, 1, asdu.QOIStation) // COT=Activation, CA=1, QOI=Station(20)
			if err != nil {
				fmt.Printf("CLIENT: Error sending Interrogation: %v\n", err)
			}

			// time.Sleep(5 * time.Second)
			// fmt.Println("CLIENT: Sending Clock Sync Command...")
			// err = c.ClockSynchronizationCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, 1, time.Now()) // COT=Activation, CA=1, Time=Now
			// if err != nil {
			// 	fmt.Printf("CLIENT: Error sending Clock Sync: %v\n", err)
			// }

			for {
				time.Sleep(50 * time.Second)
				fmt.Println("CLIENT: Sending Interrogation Command...")
				err := c.InterrogationCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, 1, asdu.QOIStation) // COT=Activation, CA=1, QOI=Station(20)
				if err != nil {
					fmt.Printf("CLIENT: Error sending Interrogation: %v\n", err)
				}
			}
		}()
	})
	client.SetConnectionLostHandler(func(c *cs101.Client, err error) {
		fmt.Printf("CLIENT: Connection lost: %v\n", err)
	})
	client.SetConnectErrorHandler(func(c *cs101.Client, err error) {
		fmt.Printf("CLIENT: Connection error: %v\n", err)
	})

	// --- Start Client ---
	fmt.Printf("CLIENT: Starting client for port %s...\n", serialPortAddress)
	err := client.Start()
	if err != nil {
		log.Fatalf("CLIENT: Failed to start client: %v", err)
	}

	// --- Keep Running & Handle Shutdown ---
	fmt.Println("CLIENT: Running... Press Ctrl+C to exit.")
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("CLIENT: Shutting down...")
	err = client.Close()
	if err != nil {
		fmt.Printf("CLIENT: Error closing client: %v\n", err)
	}
	fmt.Println("CLIENT: Shutdown complete.")
}

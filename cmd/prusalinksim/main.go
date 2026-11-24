// Command prusalinksim runs a fake PrusaLink until it is interrupted, for a
// person who wants to see the driver in a running server without a printer.
//
//	go run ./cmd/prusalinksim -api-key k3y -print-after 20s
//
// It prints the address to put in the connector endpoint. With -print-after it
// starts a print after that long, so the server shows a print without anybody
// at a printer.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/lkvapil/prusa-driver-prusalink/prusalinksim"
)

func main() {
	apiKey := flag.String("api-key", "", "the API key the printer accepts")
	user := flag.String("user", "", "the Digest user the printer asks for")
	password := flag.String("password", "", "the password of that user")
	serial := flag.String("serial", "CZPX0000X004XK00000", "the serial number the printer reports")
	printAfter := flag.Duration("print-after", 0, "start a print after this long, 0 for never")
	flag.Parse()

	printer := prusalinksim.Start(prusalinksim.Options{
		APIKey: *apiKey, User: *user, Password: *password, Serial: *serial,
	})
	defer printer.Close()

	fmt.Println("fake PrusaLink at", printer.Address()) //nolint:forbidigo // a command's own output.

	if *printAfter > 0 {
		time.AfterFunc(*printAfter, func() { printer.StartPrint(7, "T500_Adapter.gcode") })
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
}

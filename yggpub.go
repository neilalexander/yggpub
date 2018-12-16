package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/dustin/go-humanize"
)

var nodename *string
var listenaddr *string
var adminaddr *string

func main() {
	// Get the default hostname
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "Unnamed node"
	}

	// Configure the command line parameters.
	nodename = flag.String("nodename", hostname, "specify the friendly name of the node")
	adminaddr = flag.String("adminaddr", "unix:///var/run/yggdrasil.sock", "path to the admin socket")
	listenaddr = flag.String("listenaddr", "[::]:80", "address and port to listen on")
	flag.Parse()

	// Set up the HTML handlers
	http.HandleFunc("/", handler)
	http.HandleFunc("/style.css", filehandler)
	http.HandleFunc("/chartist.min.css", filehandler)
	http.HandleFunc("/chartist.min.js", filehandler)

	// Output some stuff
	log.Println("Using node name:", *nodename)
	log.Println("Using admin socket address:", *adminaddr)
	log.Println("Listening on address:", *listenaddr)

	// Start listening
	log.Fatal(http.ListenAndServe(*listenaddr, nil))
}

type switchPortData struct {
	ports   []string
	txbytes uint64
	rxbytes uint64
	coords  string
}

func filehandler(w http.ResponseWriter, r *http.Request) {
	// Load the file up and send it to the browser
	tokens := strings.Split(r.URL.Path, "/")
	b, err := ioutil.ReadFile(tokens[len(tokens)-1:][0])
	if err != nil {
		log.Fatalln(err)
	}
	w.Write(b)
}

func handler(w http.ResponseWriter, r *http.Request) {
	// Get the template HTML file
	b, err := ioutil.ReadFile("template.html")
	if err != nil {
		log.Fatalln(err)
	}

	// Parse the admin URL
	admin, err := url.Parse(*adminaddr)
	if err != nil {
		log.Fatalln(err)
		return
	}

	// Connect to the admin socket
	var a string
	if admin.Scheme == "unix" {
		a = admin.Path
	} else {
		a = admin.Host
	}
	conn, err := net.Dial(admin.Scheme, a)
	if err != nil {
		log.Println(err)
		return
	}
	defer conn.Close()

	// Create the request that we will send to the admin socket
	m := make(map[string]interface{})
	m["request"] = "getSwitchPeers"

	// Marshal the request into JSON
	j, err := json.Marshal(m)
	if err != nil {
		fmt.Fprintf(w, strings.Replace(string(b), "%PEERS%", "Unable to marshal JSON", -1))
		return
	}

	// Write the JSON to the admin socket
	conn.Write(j)

	// Create a buffer for the response
	buff := make([]byte, 65535)

	// Check for a response from the admin socket
	n, _ := conn.Read(buff)
	if n == 0 {
		fmt.Fprintf(w, strings.Replace(string(b), "%PEERS%", "No response from admin socket", -1))
		return
	}

	// Parse it back from JSON
	err = json.Unmarshal(buff[:n], &m)
	if err != nil {
		fmt.Fprintf(w, strings.Replace(string(b), "%PEERS%", "Unable to unmarshal JSON", -1))
		return
	}

	// Check if the response showed success
	if m["status"].(string) != "success" {
		fmt.Fprintf(w, strings.Replace(string(b), "%PEERS%", "Non-successful response", -1))
		return
	}

	// Create the output buffer
	var str strings.Builder

	// Print peer information
	response := m["response"].(map[string]interface{})
	peers := response["switchpeers"].(map[string]interface{})

	// Create a map of our data
	peermap := make(map[string]switchPortData)

	// Populate the peermap
	var totalbytes uint64
	for k, v := range peers {
		peer := v.(map[string]interface{})
		peerip := peer["ip"].(string)
		if peerdata, ok := peermap[peerip]; ok {
			tx, rx := uint64(peer["bytes_sent"].(float64)), uint64(peer["bytes_recvd"].(float64))
			peerdata.txbytes += tx
			peerdata.rxbytes += rx
			peerdata.ports = append(peerdata.ports, k)
			peermap[peerip] = peerdata
			totalbytes += tx + rx
		} else {
			tx, rx := uint64(peer["bytes_sent"].(float64)), uint64(peer["bytes_recvd"].(float64))
			peerdata := switchPortData{
				txbytes: tx,
				rxbytes: rx,
				coords:  peer["coords"].(string),
			}
			peerdata.ports = append(peerdata.ports, k)
			peermap[peer["ip"].(string)] = peerdata
			totalbytes += tx + rx
		}
	}

	// Render the results
	count := 0
	offset := uint64(0)
	for ipv6, peer := range peermap {
		var ports string
		if len(peer.ports) > 1 {
			ports = "switch ports " + strings.Join(peer.ports, ", ")
		} else {
			ports = "switch port " + peer.ports[0]
		}
		if peer.coords == "[]" {
			peer.coords = "Root"
		}
		str.WriteString("<div class='node'>\n")
		str.WriteString(fmt.Sprintf("<div class='ct-chart ct-perfect-fourth' id='ct-%d'></div>\n", count))
		str.WriteString(fmt.Sprintf("<script>\nnew Chartist.Pie('#ct-%d', { series: [%d, %d, %d, %d] }, { donut: true, donutWidth: 25, donutSolid: true, startbytes: 0, showLabel: false });\n</script>\n",
			count, offset, peer.txbytes, peer.rxbytes, totalbytes-offset-peer.txbytes-peer.rxbytes))
		str.WriteString(fmt.Sprintf("<div id='ipv6'>%s</div>\n", ipv6))
		str.WriteString(fmt.Sprintf("<div>%s attached to %s</div>\n", peer.coords, ports))
		str.WriteString(fmt.Sprintf("<div>%s sent</div>\n", humanize.Bytes(peer.txbytes)))
		str.WriteString(fmt.Sprintf("<div>%s received</div>\n", humanize.Bytes(peer.rxbytes)))
		str.WriteString("</div>\n")
		count++
		offset += peer.rxbytes + peer.txbytes
	}

	// No peers? Say that instead!
	if len(peermap) == 0 {
		str.WriteString("<div>There are no connected peers at this time.</div>")
	}

	// Perform some substitutions and send back the response
	b = []byte(strings.Replace(string(b), "%HOSTNAME%", *nodename, -1))
	b = []byte(strings.Replace(string(b), "%PEERS%", str.String(), -1))
	fmt.Fprintf(w, string(b))
}

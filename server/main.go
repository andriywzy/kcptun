package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"time"

	"github.com/codegangsta/cli"
	"github.com/hashicorp/yamux"
	"github.com/xtaci/kcp-go"
)

type secureConn struct {
	encoder cipher.Stream
	decoder cipher.Stream
	conn    net.Conn
}

func newSecureConn(key string, conn net.Conn, iv []byte) *secureConn {
	sc := new(secureConn)
	sc.conn = conn
	commkey := sha256.Sum256([]byte(key))

	// encoder
	block, err := aes.NewCipher(commkey[:])
	if err != nil {
		log.Println(err)
		return nil
	}
	sc.encoder = cipher.NewCFBEncrypter(block, iv)

	// decoder
	block, err = aes.NewCipher(commkey[:])
	if err != nil {
		log.Println(err)
		return nil
	}
	sc.decoder = cipher.NewCFBDecrypter(block, iv)
	return sc
}

func (sc *secureConn) Read(p []byte) (n int, err error) {
	n, err = sc.conn.Read(p)
	if err == nil {
		sc.decoder.XORKeyStream(p[:n], p[:n])
	}
	return
}

func (sc *secureConn) Write(p []byte) (n int, err error) {
	sc.encoder.XORKeyStream(p, p)
	return sc.conn.Write(p)
}

func (sc *secureConn) Close() (err error) {
	return sc.conn.Close()
}

// handle multiplex-ed connection
func handleMux(conn *kcp.UDPSession, key, target string) {
	conn.SetWindowSize(1024, 1024)
	// read iv
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(conn, iv); err != nil {
		log.Println(err)
		conn.Close()
		return
	}

	// stream multiplex
	scon := newSecureConn(key, conn, iv)
	mux, err := yamux.Server(scon, nil)
	if err != nil {
		log.Println(err)
		return
	}
	defer mux.Close()

	for {
		p1, err := mux.Accept()
		if err != nil {
			log.Println(err)
			return
		}
		p2, err := net.Dial("tcp", target)
		if err != nil {
			log.Println(err)
			return
		}
		go handleClient(p1, p2)
	}
}

func handleClient(p1, p2 net.Conn) {
	log.Println("stream opened")
	defer log.Println("stream closed")
	defer p1.Close()
	defer p2.Close()

	// start tunnel
	p1die := make(chan struct{})
	go func() {
		io.Copy(p1, p2)
		close(p1die)
	}()

	p2die := make(chan struct{})
	go func() {
		io.Copy(p2, p1)
		close(p2die)
	}()

	// wait for tunnel termination
	select {
	case <-p1die:
	case <-p2die:
	}
}

func main() {
	rand.Seed(int64(time.Now().Nanosecond()))
	myApp := cli.NewApp()
	myApp.Name = "kcptun"
	myApp.Usage = "kcptun server"
	myApp.Version = "1.0"
	myApp.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "listen,l",
			Value: ":29900",
			Usage: "kcp server listen addr:",
		},
		cli.StringFlag{
			Name:  "target, t",
			Value: "127.0.0.1:12948",
			Usage: "target server addr",
		},
		cli.StringFlag{
			Name:  "key",
			Value: "it's a secrect",
			Usage: "key for communcation, must be the same as kcptun client",
		},
		cli.StringFlag{
			Name:  "mode",
			Value: "fast",
			Usage: "mode for communication: fast, normal, default",
		},
	}
	myApp.Action = func(c *cli.Context) {
		// KCP listen
		var mode kcp.Mode
		switch c.String("mode") {
		case "normal":
			mode = kcp.MODE_NORMAL
		case "default":
			mode = kcp.MODE_DEFAULT
		case "fast":
			mode = kcp.MODE_FAST
		default:
			log.Println("unregonized mode:", c.String("mode"))
			return
		}

		lis, err := kcp.ListenEncrypted(mode, c.String("listen"), []byte(c.String("key")))
		if err != nil {
			log.Fatal(err)
		}
		log.Println("listening on ", lis.Addr())
		log.Println("communication mode:", c.String("mode"))
		for {
			if conn, err := lis.Accept(); err == nil {
				go handleMux(conn, c.String("key"), c.String("target"))
			} else {
				log.Println(err)
			}
		}
	}
	myApp.Run(os.Args)
}

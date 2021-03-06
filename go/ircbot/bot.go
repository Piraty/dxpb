/*
* Simple IRC bot to listen for messages on dxpb's published log sockets and
* forward them to IRC
 */
package main

import (
	"io/ioutil"
	"log"
	"strings"
	"strconv"
	"sync"
	"time"

	"github.com/thoj/go-ircevent"
	"github.com/zeromq/goczmq"
	"gopkg.in/yaml.v2"
)

type Chan struct {
	Name     string `yaml:"name"`
	Loglevel int    `yaml:"loglevel"`
	CanFlood bool
}

type ircMsg struct {
	txt      string
	Loglevel int
	Num      int
}

type IrcServer struct {
	irc       *irc.Connection
	Nick      string  `yaml:"nick"`
	SSL       bool    `yaml:"ssl"`
	Connstr   string  `yaml:"connstr"`
	Chans     []*Chan `yaml:"chans,flow"`
	pipe      <-chan *ircMsg
	Wait2Join float32
}

type conf struct {
	Irc       []*IrcServer `yaml:"irc,flow"`
	Endpoints []string     `yaml:"endpoints,flow"`
}

var types []string = []string{"ERROR", "NORMAL", "DEBUG", "VERBOSE", "TRACE"}

func tellchans(server *IrcServer, wg sync.WaitGroup) {
	go server.irc.Loop()
	for msg := range server.pipe {
		if msg == nil {
			break
		}
		for _, channel := range server.Chans {
			if channel.Loglevel > msg.Loglevel {
				if msg.Num > 0 {
					msg.txt = msg.txt + " (" + strconv.Itoa(msg.Num) + "x)"
				}
				server.irc.Notice(channel.Name, msg.txt)
				switch channel.CanFlood {
				case false:
					time.Sleep(2 * time.Second)
				default:
				}
			}
		}
	}
	server.irc.Disconnect()
	wg.Done()
}

func combstrs(in <-chan *ircMsg, out chan<- *ircMsg) {
	var oldmsgs []*ircMsg
	var newmsgs []*ircMsg
	timeout := make(chan bool, 1)
	timeout <- true
	for {
	L:
		select {
		case newmsg := <-in:
			for _, msg := range oldmsgs {
				if msg.Loglevel == newmsg.Loglevel && msg.txt == newmsg.txt {
					msg.Num++
					break L
				}
			}
			for _, msg := range newmsgs {
				if msg.Loglevel == newmsg.Loglevel && msg.txt == newmsg.txt {
					msg.Num++
					break L
				}
			}
			newmsgs = append(newmsgs, newmsg)
		case <-timeout:
			for _, msg := range oldmsgs {
				out <- msg
			}
			oldmsgs, newmsgs = newmsgs, oldmsgs[0:0]
			go func() {
				time.Sleep(3 * time.Second)
				timeout <- true
			}()
		}
	}
}

func initIRC(servers []*IrcServer, wg sync.WaitGroup) ([]*IrcServer, []chan<- *ircMsg) {
	var retPipes []chan<- *ircMsg
	wg.Add(len(servers))
	for _, server := range servers {
		pubpipe := make(chan *ircMsg, 64)
		go func(server *IrcServer, pubpipe chan *ircMsg) {
			server.irc = irc.IRC(server.Nick, "Lobotomy")
			server.irc.UseTLS = server.SSL
			server.irc.Connect(server.Connstr)
			if server.Wait2Join > 0 {
				time.Sleep(time.Duration(server.Wait2Join) * time.Second)
			}
			for _, curchan := range server.Chans {
				server.irc.Join(curchan.Name)
				if curchan.Loglevel > 2 {
					server.irc.Notice(curchan.Name, "Bot ready")
				}
				log.Printf("Bot attached to channel %s\n", curchan.Name)
			}
			pipe := make(chan *ircMsg, 64)
			server.pipe = pipe
			go combstrs(pubpipe, pipe)
			go tellchans(server, wg)
		}(server, pubpipe)
		retPipes = append(retPipes, pubpipe)
	}
	wg.Done()
	return servers, retPipes
}

func init0mq(endpoints []string) map[string]*goczmq.Channeler {
	socks := map[string]*goczmq.Channeler{}
	endpointstr := strings.Join(endpoints, ",")
	log.Printf("Connecting to endpoints: %s\n", endpointstr)
	for _, mode := range types {
		sock := goczmq.NewSubChanneler(endpointstr, mode)
		socks[mode] = sock
	}
	return socks
}

func prepMsgTxt(prefix byte, in [][]byte) []string {
	var ret []string = make([]string, len(in)+1)
	ret[0] = string(prefix)
	for i, bytes := range in {
		ret[i+1] = string(bytes)
	}
	return ret
}

func sockToPipes(sock *goczmq.Channeler, pipes []chan<- *ircMsg, loglevel int, wg sync.WaitGroup) {
	defer sock.Destroy()
	for {
		msg := <-sock.RecvChan
		out := new(ircMsg)
		out.Loglevel = loglevel
		switch len(msg) {
		case 0:
			out.txt = "Got channel error"
		case 1:
			out.txt = string(msg[0])
		default:
			out.txt = strings.Join(prepMsgTxt(msg[0][0], msg[1:]), ": ")
		}
		for _, pipe := range pipes {
			pipe <- out
		}
	}
	log.Printf("Done reading socks for loglevel %d\n", loglevel)
	wg.Done()
}

func startSocks(socks map[string]*goczmq.Channeler, pipes []chan<- *ircMsg, wg sync.WaitGroup) {
	wg.Add(len(types))
	for loglevel, mode := range types {
		go sockToPipes(socks[mode], pipes, loglevel, wg)
	}
	wg.Done()
}

func (config *conf) read(filename string) *conf {
	confFile, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Printf("confFile.Get err   #%v ", err)
	}

	err = yaml.UnmarshalStrict(confFile, &config)
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	return config
}

func main() {
	config := conf{}
	config.read("./conf.yml")

	var wg sync.WaitGroup
	var pipes []chan<- *ircMsg
	wg.Add(1)
	config.Irc, pipes = initIRC(config.Irc, wg)
	socks := init0mq(config.Endpoints)
	log.Println("Starting logger")
	startSocks(socks, pipes, wg)
	wg.Wait()
}

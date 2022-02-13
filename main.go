package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-systemd/activation"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	cli "github.com/urfave/cli/v2"
	"tailscale.com/client/tailscale"
)

func main() {
	app := cli.App{
		Commands: []*cli.Command{
			{
				Name:   "daemon",
				Usage:  "run the livemon daemon",
				Action: serve,
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "tailscale-only",
						Usage: "only allow metrics collection over Tailscale",
					},
					&cli.BoolFlag{
						Name:  "dev",
						Usage: "dev mode: listen on localhost:9843 and filemon.sock in current dir",
					},
				},
			},
			{
				Name:   "poke",
				Usage:  "poke a unit",
				Action: poke,
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "sock",
						Usage: "socket to talk to livemon",
						Value: "/run/livemon/livemon.sock",
					},
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func poke(c *cli.Context) error {
	if c.Args().Len() != 2 {
		return fmt.Errorf("usage error, need 2 args")
	}

	sockPath := c.String("sock")
	if sockPath == "" {
		return fmt.Errorf("no --sock provided")
	}

	unit, state := c.Args().Get(0), c.Args().Get(1)

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return fmt.Errorf("dialing livemon: %v", err)
	}
	defer conn.Close()

	if _, err := fmt.Fprintf(conn, "%s %s\n", unit, state); err != nil {
		return fmt.Errorf("poking livemon: %v", err)
	}

	var b [1]byte
	// Only used for blocking until livemon's finished.
	conn.Read(b[:])
	return nil
}

func serve(c *cli.Context) error {
	var (
		httpLn, ctlLn net.Listener
		stateDir      string
		err           error
	)

	if c.Bool("dev") {
		stateDir = "."
		httpLn, err = net.Listen("tcp", "[::1]:9843")
		if err != nil {
			return err
		}
		os.Remove("livemon.sock")
		ctlLn, err = net.Listen("unix", "livemon.sock")
		if err != nil {
			return err
		}
		log.Println("running in dev mode")
	} else {
		stateDir = os.Getenv("STATE_DIRECTORY")
		if stateDir == "" {
			return fmt.Errorf("missing STATE_DIRECTORY")
		}
		listeners, err := activation.Listeners()
		if err != nil {
			return fmt.Errorf("getting listeners from systemd: %v", err)
		}
		if len(listeners) != 2 {
			return fmt.Errorf("wrong size FD vector from systemd: got %d, want 2", len(listeners))
		}
		httpLn, ctlLn = listeners[0], listeners[1]
	}

	statePath := filepath.Join(stateDir, "livemon.state")

	state, err := loadState(statePath)
	if err != nil {
		return fmt.Errorf("loading state: %v", err)
	}

	s := &Server{
		statePath:     statePath,
		tailscaleOnly: c.Bool("tailscale-only"),
		lastTouched: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "livemon",
			Name:      "last_touched",
			Help:      "timestamp of the last time a unit was poked",
		}, []string{"unit"}),
		lastSuccess: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "livemon",
			Name:      "last_success",
			Help:      "timestamp of the last time a unit succeeded",
		}, []string{"unit"}),
		st: state,
	}
	return s.Serve(httpLn, ctlLn)
}

type Server struct {
	statePath     string
	tailscaleOnly bool

	sync.Mutex
	st          *State
	lastTouched *prometheus.GaugeVec
	lastSuccess *prometheus.GaugeVec
}

func (s *Server) Serve(httpLn, ctlLn net.Listener) error {
	s.Lock()
	s.updateMetricsLocked()
	s.Unlock()

	http.Handle("/metrics", promhttp.Handler())

	errc := make(chan error, 2)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.tailscaleOnly {
				who, err := tailscale.WhoIs(r.Context(), r.RemoteAddr)
				if err != nil || who.UserProfile == nil {
					http.Error(w, "access denied", http.StatusForbidden)
					return
				}
			}
			http.DefaultServeMux.ServeHTTP(w, r)
		}),
	}
	go func() {
		if err := srv.Serve(httpLn); err != nil {
			errc <- err
		}
	}()
	go func() {
		errc <- s.serveCtl(ctlLn)
	}()

	return <-errc
}

func (s *Server) updateMetricsLocked() {
	for name, unit := range s.st.Units {
		if !unit.LastTouched.IsZero() {
			s.lastTouched.With(prometheus.Labels{"unit": name}).Set(float64(unit.LastTouched.Unix()))
		}
		if !unit.LastSuccess.IsZero() {
			s.lastSuccess.With(prometheus.Labels{"unit": name}).Set(float64(unit.LastSuccess.Unix()))
		}
	}
}

func (s *Server) serveCtl(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			if err := s.poke(c); err != nil {
				log.Printf("handling client conn: %v", err)
			}
		}()
	}
}

func (s *Server) poke(c net.Conn) error {
	defer c.Close()
	br := bufio.NewReader(c)
	l, err := br.ReadString('\n')
	if err != nil {
		return err
	}
	fs := strings.Fields(l)
	if len(fs) != 2 {
		return fmt.Errorf("invalid command string %q", strings.TrimSpace(l))
	}
	unit := fs[0]
	status, err := strconv.Atoi(fs[1])
	if err != nil {
		log.Printf("invalid status %q, assuming failure: %v", fs[1], err)
		status = 255
	}
	s.Lock()
	defer s.Unlock()

	u := s.st.Units[unit]
	if u == nil {
		u = &Unit{}
		s.st.Units[unit] = u
	}
	t := time.Now()
	u.LastTouched = t
	if status == 0 {
		u.LastSuccess = t
	}
	s.updateMetricsLocked()

	return saveState(s.statePath, s.st)
}

type State struct {
	Created time.Time // when was the state first made
	Units   map[string]*Unit
}

type Unit struct {
	LastTouched time.Time
	LastSuccess time.Time
}

func loadState(path string) (*State, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		st := &State{
			Created: time.Now(),
			Units:   map[string]*Unit{},
		}
		if err := saveState(path, st); err != nil {
			return nil, fmt.Errorf("creating initial state: %v", err)
		}
	}

	bs, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading state from %q: %v", path, err)
	}

	var st State
	if err := json.Unmarshal(bs, &st); err != nil {
		return nil, fmt.Errorf("decoding state from %q: %v", path, err)
	}

	return &st, nil
}

func saveState(path string, st *State) error {
	bs, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicWrite(path, bs, 0600)
}

func atomicWrite(path string, content []byte, perm fs.FileMode) error {
	tmp := path + ".tmp"
	os.Remove(tmp)
	if err := os.WriteFile(tmp, content, perm); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}

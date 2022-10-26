package httpd

import (
	"encoding/json"
	"fmt"
	"github.com/AllenShaw19/raft_grpc/store"
	"github.com/hashicorp/raft"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type Store interface {
	// Get returns the value for the given key.
	Get(id int, key string, lvl store.ConsistencyLevel) (string, error)

	// Set sets the value for the given key, via distributed consensus.
	Set(id int, key, value string) error

	// Delete removes the given key, via distributed consensus.
	Delete(id int, key string) error

	// Join joins the node, identified by nodeID and reachable at addr, to the cluster.
	Join(nodeID string, httpAddr string, addr string) error

	LeaderAPIAddr(id int) string

	SetMeta(key, value string)
}

type Service struct {
	ln    net.Listener
	store Store
	addr  string

	logger *log.Logger
}

func New(addr string, store Store) *Service {
	return &Service{
		addr:   addr,
		store:  store,
		logger: log.New(os.Stdout, "[httpd] ", log.LstdFlags),
	}
}

func (s *Service) Start() error {
	server := http.Server{
		Handler: s,
	}

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.ln = ln

	http.Handle("/", s)

	go func() {
		err := server.Serve(s.ln)
		if err != nil {
			s.logger.Fatalf("HTTP serve: %s", err)
		}
	}()

	return nil
}

func (s *Service) Close() {
	s.ln.Close()
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("path: %v\n", r.URL.Path)
	if strings.HasPrefix(r.URL.Path, "/key") {
		s.handleKeyRequest(w, r)
	} else if r.URL.Path == "/join" {
		s.handleJoin(w, r)
	} else {
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Service) handleJoin(w http.ResponseWriter, r *http.Request) {
	m := map[string]string{}
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if len(m) != 3 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	httpAddr, ok := m["httpAddr"]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	raftAddr, ok := m["raftAddr"]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	nodeID, ok := m["id"]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if err := s.store.Join(nodeID, httpAddr, raftAddr); err != nil {
		if err == raft.ErrNotLeader {
			leader := s.store.LeaderAPIAddr(0)
			if leader == "" {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			redirect := s.FormRedirect(r, leader)
			http.Redirect(w, r, redirect, http.StatusTemporaryRedirect)
			return
		}

		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func (s *Service) FormRedirect(r *http.Request, host string) string {
	protocol := "http"
	rq := r.URL.RawQuery
	if rq != "" {
		rq = fmt.Sprintf("?%s", rq)
	}
	return fmt.Sprintf("%s://%s%s%s", protocol, host, r.URL.Path, rq)
}

func level(req *http.Request) (store.ConsistencyLevel, error) {
	q := req.URL.Query()
	lvl := strings.TrimSpace(q.Get("level"))

	switch strings.ToLower(lvl) {
	case "default":
		return store.Default, nil
	case "stale":
		return store.Stale, nil
	case "consistent":
		return store.Consistent, nil
	default:
		return store.Default, nil
	}
}

func (s *Service) handleKeyRequest(w http.ResponseWriter, r *http.Request) {
	getKey := func() string {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) != 4 {
			w.WriteHeader(http.StatusBadRequest)
			return ""
		}
		return parts[3]
	}
	getID := func() (int, error) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 3 {
			w.WriteHeader(http.StatusBadRequest)
			return 0, fmt.Errorf("invalid paths %v", parts)
		}
		id := parts[2]
		return strconv.Atoi(id)
	}

	fmt.Printf("url %v, method %v\n", r.URL.Path, r.Method)
	switch r.Method {
	case "GET":
		k := getKey()
		if k == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		id, err := getID()
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		lvl, err := level(r)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		v, err := s.store.Get(id, k, lvl)
		if err != nil {
			if err == raft.ErrNotLeader {
				leader := s.store.LeaderAPIAddr(id)
				if leader == "" {
					http.Error(w, err.Error(), http.StatusServiceUnavailable)
					return
				}
				redirect := s.FormRedirect(r, leader)
				http.Redirect(w, r, redirect, http.StatusTemporaryRedirect)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		b, err := json.Marshal(map[string]string{k: v})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if _, err := io.WriteString(w, string(b)); err != nil {
			s.logger.Printf("faile to WriteString: %v", err)
		}

	case "POST":

		id, err := getID()
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Read the value from the POST body.
		m := map[string]string{}
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for k, v := range m {
			if err := s.store.Set(id, k, v); err != nil {
				if err == raft.ErrNotLeader {
					leader := s.store.LeaderAPIAddr(id)
					if leader == "" {
						http.Error(w, err.Error(), http.StatusServiceUnavailable)
						return
					}

					redirect := s.FormRedirect(r, leader)
					http.Redirect(w, r, redirect, http.StatusTemporaryRedirect)
					return
				}

				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}

	case "DELETE":
		id, err := getID()
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		k := getKey()
		if k == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if err := s.store.Delete(id, k); err != nil {
			if err == raft.ErrNotLeader {
				leader := s.store.LeaderAPIAddr(id)
				if leader == "" {
					http.Error(w, err.Error(), http.StatusServiceUnavailable)
					return
				}
				redirect := s.FormRedirect(r, leader)
				http.Redirect(w, r, redirect, http.StatusTemporaryRedirect)
				return
			}

			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		s.store.Delete(id, k)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// Addr returns the address on which the Service is listening
func (s *Service) Addr() net.Addr {
	return s.ln.Addr()
}

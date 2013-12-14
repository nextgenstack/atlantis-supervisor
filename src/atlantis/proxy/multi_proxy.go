package proxy

import (
	. "atlantis/proxy/types"
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/cespare/go-apachelog"
	"github.com/gorilla/mux"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

const (
	NotProxyingError = iota
	AlreadyProxyingError
)

type MultiProxyError struct {
	Msg  string
	Code int
}

func (e *MultiProxyError) Error() string {
	return e.Msg
}

func NewNotProxyingError(lAddr string) *MultiProxyError {
	return &MultiProxyError{Msg: "Not Proxying " + lAddr, Code: NotProxyingError}
}

func NewAlreadyProxyingError(lAddr, rAddr string) *MultiProxyError {
	return &MultiProxyError{Msg: "Already Proxying " + lAddr + " to " + rAddr, Code: NotProxyingError}
}

type MultiProxy struct {
	sync.Mutex
	SaveFile           string
	ConfigAddr         string
	DefaultNumHandlers int
	DefaultMaxPending  int
	ProxyConfigs       map[string]*ProxyConfig
	proxyMap           map[string]Proxy // local address -> proxy
}

func NewMultiProxy(saveFile, cAddr string, numHandlers, maxPending int) *MultiProxy {
	return &MultiProxy{
		Mutex:              sync.Mutex{},
		SaveFile:           saveFile,
		ConfigAddr:         cAddr,
		DefaultNumHandlers: numHandlers,
		DefaultMaxPending:  maxPending,
		ProxyConfigs:       map[string]*ProxyConfig{},
		proxyMap:           map[string]Proxy{},
	}
}

func (p *MultiProxy) AddProxy(cfg *ProxyConfig) error {
	p.Lock()
	defer p.Unlock()
	if proxy, ok := p.proxyMap[cfg.LocalAddr]; ok && proxy != nil {
		return NewAlreadyProxyingError(cfg.LocalAddr, proxy.RemoteAddr())
	}
	return p.add(cfg)
}

func (p *MultiProxy) RemoveProxy(localAddr string) error {
	p.Lock()
	defer p.Unlock()
	if proxy, ok := p.proxyMap[localAddr]; !ok || proxy == nil {
		return NewNotProxyingError(localAddr)
	} else {
		p.remove(proxy)
	}
	return nil
}

func (p *MultiProxy) Listen() error {
	p.load()
	log.Printf("[CONFIG] MultiProxy Initializing")
	log.Printf("[CONFIG]   - SaveFile:           %s", p.SaveFile)
	log.Printf("[CONFIG]   - ConfigAddr:         %s", p.ConfigAddr)
	log.Printf("[CONFIG]   - DefaultNumHandlers: %d", p.DefaultNumHandlers)
	log.Printf("[CONFIG]   - DefaultMaxPending:  %d", p.DefaultMaxPending)
	// listen for config changes
	gmux := mux.NewRouter() // Use gorilla mux for APIs to make things easier
	gmux.HandleFunc("/proxy/{local}/{remote}", p.AddProxyHandler).Methods("PUT")
	gmux.HandleFunc("/proxy/{local}/{remote}", p.RemoveProxyHandler).Methods("DELETE")
	gmux.HandleFunc("/proxy/{local}", p.RemoveProxyHandler).Methods("DELETE")
	gmux.HandleFunc("/config", p.GetConfigHandler).Methods("GET")
	gmux.HandleFunc("/config", p.PatchConfigHandler).Methods("PATCH")

	server := &http.Server{Addr: p.ConfigAddr, Handler: apachelog.NewHandler(gmux, os.Stderr)}
	log.Printf("[CONFIG] listening on %s", p.ConfigAddr)
	log.Fatal(server.ListenAndServe())
	return nil
}

func (p *MultiProxy) AddProxyHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	local := sanitizeAddr(vars["local"])
	remote := sanitizeAddr(vars["remote"])
	var cfg ProxyConfig
	dec := json.NewDecoder(r.Body)
	err := dec.Decode(&cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg.LocalAddr = local
	cfg.RemoteAddr = remote
	err = p.AddProxy(&cfg)
	if err != nil {
		switch err.(type) {
		case *MultiProxyError:
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	p.save()
	log.Printf("[CONFIG] added %s -> %s", local, remote)
	fmt.Fprintf(w, "added %s -> %s\n", local, remote)
}

func (p *MultiProxy) RemoveProxyHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	local := sanitizeAddr(vars["local"])
	if err := p.RemoveProxy(local); err != nil {
		switch err.(type) {
		case *MultiProxyError:
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	p.save()
	log.Printf("[CONFIG] removed %s", local)
	fmt.Fprintf(w, "removed %s\n", local)
}

func (p *MultiProxy) GetConfigHandler(w http.ResponseWriter, r *http.Request) {
	enc := json.NewEncoder(w)
	p.Lock()
	defer p.Unlock()
	enc.Encode(p.ProxyConfigs)
}

func (p *MultiProxy) PatchConfigHandler(w http.ResponseWriter, r *http.Request) {
	p.Lock()
	defer p.Unlock()
	var body map[string]*ProxyConfig
	dec := json.NewDecoder(r.Body)
	err := dec.Decode(&body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// add the stuff we need to
	for lAddr, cfg := range body {
		if pxy := p.proxyMap[lAddr]; pxy != nil && cfg.LocalAddr == pxy.LocalAddr() &&
			cfg.RemoteAddr == pxy.RemoteAddr() {
			continue // same thing
		} else if pxy != nil {
			// not the same thing, kill the proxy then restart it.
			// kill
			p.remove(pxy)
			// restart
			if err := p.add(cfg); err != nil {
				log.Printf("[CONFIG] ERROR: %v", err)
			}
		} else {
			if err := p.add(cfg); err != nil {
				log.Printf("[CONFIG] ERROR: %v", err)
			}
		}
	}
	// remove the stuff we need to
	for lAddr, pxy := range p.proxyMap {
		if cfg := body[lAddr]; cfg != nil {
			continue // should be the same thing now
		} else { // cfg == nil, we need to delete
			p.remove(pxy)
		}
	}
}

func (p *MultiProxy) remove(pxy Proxy) {
	pxy.Die()
	delete(p.proxyMap, pxy.LocalAddr())
	delete(p.ProxyConfigs, pxy.LocalAddr())
}

func (p *MultiProxy) add(cfg *ProxyConfig) error {
	if cfg.NumHandlers <= 0 {
		cfg.NumHandlers = p.DefaultNumHandlers
	}
	if cfg.MaxPending <= 0 {
		cfg.MaxPending = p.DefaultMaxPending
	}
	proxy := NewProxyWithConfig(cfg)
	if err := proxy.Init(); err != nil {
		return err
	}
	p.proxyMap[cfg.LocalAddr] = proxy
	p.ProxyConfigs[cfg.LocalAddr] = cfg
	go proxy.Listen()
	return nil
}

func sanitizeAddr(addr string) string {
	if strings.Index(addr, ":") < 0 {
		return addr + ":80"
	}
	return addr
}

func (p *MultiProxy) save() {
	p.Lock()
	defer p.Unlock()
	fo, err := os.Create(p.SaveFile)
	if err != nil {
		log.Printf("[CONFIG] could not save %s: %s", p.SaveFile, err)
		return
	}
	defer fo.Close()
	w := bufio.NewWriter(fo)
	e := json.NewEncoder(w)
	e.Encode(p)
	w.Flush()
}

func (p *MultiProxy) load() {
	p.Lock()
	defer p.Unlock()
	fi, err := os.Open(p.SaveFile)
	if err != nil {
		log.Printf("[CONFIG] could not retrieve %s: %s", p.SaveFile, err)
	}
	r := bufio.NewReader(fi)
	d := json.NewDecoder(r)
	d.Decode(p)
	for _, cfg := range p.ProxyConfigs {
		log.Printf("[CONFIG] retrieve found: %s", cfg.LocalAddr)
		if err := p.add(cfg); err != nil {
			panic(err)
		}
	}
}

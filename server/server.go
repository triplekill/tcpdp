package server

import (
	"context"
	"net"
	"strings"
	"sync"

	"github.com/k1LoW/tcprxy/dumper"
	"github.com/lestrrat-go/server-starter/listener"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Server struct
type Server struct {
	listenAddr *net.TCPAddr
	remoteAddr *net.TCPAddr
	ctx        context.Context
	shutdown   context.CancelFunc
	Wg         *sync.WaitGroup
	ClosedChan chan struct{}
	listener   *net.TCPListener
	logger     *zap.Logger
	dumper     dumper.Dumper
}

// NewServer returns a new Server
func NewServer(ctx context.Context, lAddr, rAddr *net.TCPAddr, logger *zap.Logger) *Server {
	innerCtx, shutdown := context.WithCancel(ctx)
	wg := &sync.WaitGroup{}
	closedChan := make(chan struct{})

	var d dumper.Dumper
	dumpType := viper.GetString("proxy.dumper")

	switch dumpType {
	case "hex":
		d = dumper.NewHexDumper()
	case "pg":
		d = dumper.NewPgDumper()
	case "mysql":
		d = dumper.NewMysqlDumper()
	default:
		d = dumper.NewHexDumper()
	}

	return &Server{
		listenAddr: lAddr,
		remoteAddr: rAddr,
		ctx:        innerCtx,
		shutdown:   shutdown,
		Wg:         wg,
		ClosedChan: closedChan,
		logger:     logger,
		dumper:     d,
	}
}

// Start server.
func (s *Server) Start() error {
	useServerSterter := viper.GetBool("proxy.useServerSterter")

	if useServerSterter {
		listeners, err := listener.ListenAll()
		if listeners == nil || err != nil {
			s.logger.WithOptions(zap.AddCaller()).Fatal("server-starter listen error", zap.Error(err))
			return err
		}
		lt := listeners[0].(*net.TCPListener)
		s.listener = lt
	} else {
		lt, err := net.ListenTCP("tcp", s.listenAddr)
		if err != nil {
			s.logger.WithOptions(zap.AddCaller()).Fatal("listenAddr ListenTCP error", zap.Error(err))
			return err
		}
		s.listener = lt
	}

	defer func() {
		s.listener.Close()
		close(s.ClosedChan)
	}()

	for {
		conn, err := s.listener.AcceptTCP()
		if err != nil {
			if ne, ok := err.(net.Error); ok {
				if ne.Temporary() {
					continue
				}
				if !strings.Contains(err.Error(), "use of closed network connection") {
					select {
					case <-s.ctx.Done():
						break
					default:
						s.logger.WithOptions(zap.AddCaller()).Fatal("listener AcceptTCP error", zap.Error(err))
					}
				}
			}
			return err
		}
		s.Wg.Add(1)
		go s.handleConn(conn)
	}
}

// Shutdown server.
func (s *Server) Shutdown() {
	select {
	case <-s.ctx.Done():
	default:
		s.shutdown()
		s.listener.Close()
	}
}

// GracefulShutdown server.
func (s *Server) GracefulShutdown() {
	select {
	case <-s.ctx.Done():
	default:
		s.listener.Close()
	}
}

func (s *Server) handleConn(conn *net.TCPConn) {
	defer s.Wg.Done()

	remoteConn, err := net.DialTCP("tcp", nil, s.remoteAddr)
	if err != nil {
		fields := s.fieldsWithErrorAndConn(err, conn)
		s.logger.WithOptions(zap.AddCaller()).Error("remoteAddr DialTCP error", fields...)
		conn.Close()
		return
	}

	p := NewProxy(s, conn, remoteConn)
	p.Start()
}

func (s *Server) fieldsWithErrorAndConn(err error, conn *net.TCPConn) []zapcore.Field {
	fields := []zapcore.Field{
		zap.Error(err),
		zap.String("client_addr", conn.RemoteAddr().String()),
		zap.String("proxy_listen_addr", conn.LocalAddr().String()),
		zap.String("remote_addr", s.remoteAddr.String()),
	}
	return fields
}

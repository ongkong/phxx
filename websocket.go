package phx

import (
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"net/url"
	"path"
	"sync"
	"time"
)

type Websocket struct {
	dialer          *websocket.Dialer
	handler         TransportHandler
	conn            *websocket.Conn
	endPoint        string
	requestHeader   http.Header
	done            chan any
	close           chan bool
	reconnect       chan bool
	send            chan Message
	connectionTries int
	mu              sync.RWMutex
	started         bool
	closing         bool
	reconnecting    bool
}

func NewWebsocket(dialer *websocket.Dialer, handler TransportHandler) *Websocket {
	return &Websocket{
		dialer:  dialer,
		handler: handler,
	}
}

func (w *Websocket) Connect(endPoint url.URL, requestHeader http.Header) error {
	if w.isStarted() {
		return errors.New("connect was already called")
	}

	w.startup(endPoint, requestHeader)
	return nil
}

func (w *Websocket) Disconnect() error {
	if !w.isStarted() {
		return errors.New("not connected")
	}

	if w.connIsSet() {
		w.sendClose()
	} else {
		w.teardown()
	}
	return nil
}

func (w *Websocket) IsConnected() bool {
	return w.connIsReady()
}

func (w *Websocket) Send(msg Message) {
	w.send <- msg
}

func (w *Websocket) startup(endPoint url.URL, requestHeader http.Header) {
	//fmt.Println("startup", endPoint, requestHeader)

	endPoint.Path = path.Join(endPoint.Path, "websocket")

	w.endPoint = endPoint.String()
	w.requestHeader = requestHeader

	w.connectionTries = 0

	w.done = make(chan any)
	w.close = make(chan bool)
	w.reconnect = make(chan bool)
	w.send = make(chan Message, messageQueueLength)

	w.setReconnecting(false)
	w.setClosing(false)

	go w.connectionManager()
	go w.writer()
	go w.reader()

	w.setStarted(true)
}

func (w *Websocket) teardown() {
	//fmt.Println("teardown")

	// Tell the goroutines to exit
	close(w.done)
	close(w.close)
	close(w.reconnect)
	close(w.send)

	w.setStarted(false)
	w.setReconnecting(false)
	w.setClosing(false)
}

func (w *Websocket) dial() error {
	conn, _, err := w.dialer.Dial(w.endPoint, w.requestHeader)
	if err != nil {
		return err
	}
	//w.socket.Logger.Debugf("Connected conn: %+v\n\n", conn)
	//w.socket.Logger.Debugf("Connected resp: %+v\n", resp)

	w.setConn(conn)
	w.setReconnecting(false)
	w.handler.OnConnOpen()

	return nil
}

func (w *Websocket) closeConn() {
	fmt.Println("closeConn")
	if !w.connIsSet() {
		return
	}

	w.setClosing(true)

	// attempt to gracefully close the connection by sending a close websocket message
	err := w.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if err == nil {
		time.Sleep(250 * time.Millisecond)
	}

	err = w.conn.Close()
	if err != nil {
		w.handler.OnConnError(err)
	}

	w.setConn(nil)
	w.handler.OnConnClose()
	w.setClosing(false)
}

func (w *Websocket) writeToConn(msg *Message) error {
	if !w.connIsReady() {
		return errors.New("connection is not open")
	}

	return w.conn.WriteJSON(msg)
}

func (w *Websocket) readFromConn(msg *Message) error {
	if !w.connIsReady() {
		return errors.New("connection is not open")
	}

	return w.conn.ReadJSON(msg)
}

func (w *Websocket) connectionManager() {
	//fmt.Println("connectionManager started")
	//defer fmt.Println("connectionManager stopped")

	for {
		// Check if we have been told to finish
		select {
		case <-w.done:
			return
		default:
		}

		if !w.isClosing() && !w.connIsSet() {
			err := w.dial()
			if err != nil {
				w.handler.OnConnError(err)
				w.setReconnecting(true)
				delay := w.handler.ReconnectAfter(w.connectionTries)
				w.connectionTries++
				time.Sleep(delay)
				continue
			}
		}

		select {
		case <-w.done:
			return
		case <-w.close:
			w.closeConn()
			w.teardown()
		case <-w.reconnect:
			w.closeConn()
		}
	}
}

func (w *Websocket) writer() {
	//fmt.Println("writer started")
	//defer fmt.Println("writer stopped")

	for {
		// Check if we have been told to finish
		select {
		case <-w.done:
			return
		default:
		}

		if !w.connIsReady() {
			time.Sleep(busyWait)
			continue
		}

		//fmt.Println("Ready to write to socket")

		select {
		case <-w.done:
			return
		case msg := <-w.send:
			// If there is a message to send, but we're not connected, then wait until we are.
			if !w.connIsReady() {
				time.Sleep(busyWait)
				continue
			}

			// Send the message
			err := w.writeToConn(&msg)

			// If there were any errors sending, then tell the connectionManager to reconnect
			if err != nil {
				w.handler.OnWriteError(err)
				w.sendReconnect()
				time.Sleep(busyWait)
				continue
			}
		}
	}
}

func (w *Websocket) reader() {
	//fmt.Println("reader started")
	//defer fmt.Println("reader stopped")

	for {
		// Check if we have been told to finish
		select {
		case <-w.done:
			//fmt.Println("reader stopping")
			return
		default:
		}

		var msg Message

		// Wait until we're connected
		if !w.connIsReady() {
			time.Sleep(busyWait)
			continue
		}

		//fmt.Println("Ready to read from socket")

		// Read the next message from the websocket. This blocks until there is a message or error
		err := w.readFromConn(&msg)

		// If there were any errors, tell the connectionManager to reconnect
		if err != nil {
			//fmt.Printf("read error %e %v\n", err, err)
			if !websocket.IsCloseError(err, 1000) {
				w.handler.OnReadError(err)
				w.sendReconnect()
			}

			time.Sleep(busyWait)
			continue
		}

		w.handler.OnConnMessage(msg)
	}
}

func (w *Websocket) setStarted(started bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.started = started
}

func (w *Websocket) isStarted() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.started
}

func (w *Websocket) setClosing(closing bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.closing = closing
}

func (w *Websocket) isClosing() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.closing
}

func (w *Websocket) sendClose() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closing == true {
		return
	}

	w.closing = true
	w.close <- true
}

func (w *Websocket) setReconnecting(reconnecting bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.reconnecting = reconnecting
}

func (w *Websocket) isReconnecting() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.reconnecting
}

func (w *Websocket) sendReconnect() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.reconnecting || w.closing {
		return
	}

	w.reconnecting = true
	w.reconnect <- true
}

func (w *Websocket) setConn(conn *websocket.Conn) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.conn = conn
}

func (w *Websocket) connIsSet() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.conn != nil
}

func (w *Websocket) connIsReady() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.started && !w.closing && !w.reconnecting && w.conn != nil
}

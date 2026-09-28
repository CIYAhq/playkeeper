package agentclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// maxPageAnswer bounds the agent's answer about the public page's ports.
const maxPageAnswer = 64 << 10

// PublicPagePorts asks the local agent for ports 443 and 80 for the public
// page: what each port is, and the listening sockets of the ones it
// opened, HTTPS first, as files the caller owns. The sockets travel over
// the agent's Unix socket, so a client through a machine link can't ask.
func (c *Client) PublicPagePorts(ctx context.Context, want api.PagePortsRequest) (api.PublicPagePorts, []*os.File, error) {
	var out api.PublicPagePorts
	if c.socket == "" {
		return out, nil, errors.New("only the local agent hands over the public page's ports")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.socket)
	if err != nil {
		return out, nil, errors.Join(ErrUnavailable, err)
	}
	defer conn.Close()
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	body, err := json.Marshal(want)
	if err != nil {
		return out, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agent/v1/public-page/ports", bytes.NewReader(body))
	if err != nil {
		return out, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Close = true
	if err := req.Write(conn); err != nil {
		return out, nil, errors.Join(ErrUnavailable, err)
	}
	data, files, err := readWithFiles(conn.(*net.UnixConn))
	if err != nil {
		closeFiles(files)
		return out, nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(data)), req)
	if err != nil {
		closeFiles(files)
		return out, nil, errors.Join(ErrBadAnswer, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		closeFiles(files)
		return out, nil, DecodeError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		closeFiles(files)
		return out, nil, errors.Join(ErrBadAnswer, err)
	}
	return out, files, nil
}

// readWithFiles reads the connection to its end, with the files passed
// along the way.
func readWithFiles(uc *net.UnixConn) ([]byte, []*os.File, error) {
	var data []byte
	var files []*os.File
	buf := make([]byte, 16<<10)
	oob := make([]byte, syscall.CmsgSpace(8*4))
	for {
		n, oobn, flags, _, err := uc.ReadMsgUnix(buf, oob)
		if oobn > 0 {
			got, perr := parseFiles(oob[:oobn])
			files = append(files, got...)
			if perr != nil {
				return nil, files, errors.Join(ErrBadAnswer, perr)
			}
		}
		if flags&syscall.MSG_CTRUNC != 0 {
			return nil, files, errors.Join(ErrBadAnswer, errors.New("the agent passed more sockets than asked for"))
		}
		data = append(data, buf[:n]...)
		if len(data) > maxPageAnswer {
			return nil, files, errors.Join(ErrBadAnswer, errors.New("the agent's answer is too long"))
		}
		if errors.Is(err, io.EOF) || (err == nil && n == 0) {
			return data, files, nil
		}
		if err != nil {
			return nil, files, errors.Join(ErrUnavailable, err)
		}
	}
}

// parseFiles makes files of the descriptors in a control message.
func parseFiles(oob []byte) ([]*os.File, error) {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, err
	}
	var files []*os.File
	for _, m := range msgs {
		fds, err := syscall.ParseUnixRights(&m)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			files = append(files, os.NewFile(uintptr(fd), "public-page-listener"))
		}
	}
	return files, nil
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		f.Close()
	}
}

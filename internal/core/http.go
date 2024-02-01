package core

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func sendHTTPRequest(requestUrl string) (statusCode int, responseBody string, err error) {
	// Parse the URL
	u, err := url.Parse(requestUrl)
	if err != nil {
		return -1, "", err
	}

	// Establish a TCP connection with the server
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		return -1, "", err
	}
	defer conn.Close()

	// Send the HTTP Get request
	getRequest := fmt.Sprintf("GET %s?%s HTTP/1.1\r\nHost: %s\r\nAccept: text/html\r\nConnection: keep-alive\r\n\r\n", u.Path, u.RawQuery, u.Host)
	_, err = conn.Write([]byte(getRequest))
	if err != nil {
		return -1, "", err
	}

	// Get the response
	response := make([]byte, 0)
	for {
		temp := make([]byte, 1024)
		numBytesRead, err := conn.Read(temp)
		if err != nil {
			if err != io.EOF {
				return -1, "", err
			}
			break
		}
		response = append(response, temp[:numBytesRead]...)
	}

	statusCodeLine := strings.Split(string(response), "\r\n")[0]
	statusCode, err = strconv.Atoi(strings.Split(statusCodeLine, " ")[1])
	if err != nil {
		return -1, "", err
	}
	responseBody = strings.Split(string(response), "\r\n\r\n")[1]

	return statusCode, responseBody, nil
}

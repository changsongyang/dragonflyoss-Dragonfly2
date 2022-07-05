/*
 *     Copyright 2020 The Dragonfly Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

//go:generate mockgen -destination mocks/mock_source_client.go -source source_client.go -package mocks

package source

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	logger "d7y.io/dragonfly/v2/internal/dflog"
)

var (
	// client back-to-source timeout for metadata
	contextTimeout = 1 * time.Minute
)

var (
	// ErrResourceNotReachable represents the url resource is a not reachable.
	ErrResourceNotReachable = errors.New("resource is not reachable")

	// ErrNoClientFound represents no source client to resolve url
	ErrNoClientFound = errors.New("no source client found")

	// ErrClientNotSupportList represents the source client not support list action
	ErrClientNotSupportList = errors.New("source client not support list")

	// ErrClientNotSupportGetMetadata represents the source client not support get metadata
	ErrClientNotSupportGetMetadata = errors.New("source client not support get metadata")
)

// UnexpectedStatusCodeError is returned when a source responds with neither an error
// nor with a status code indicating success.
type UnexpectedStatusCodeError struct {
	allowed []int // The expected stats code returned from source
	got     int   // The actual status code from source
}

// Error implements interface error
func (e UnexpectedStatusCodeError) Error() string {
	var expected []string
	for _, v := range e.allowed {
		expected = append(expected, strconv.Itoa(v))
	}
	return fmt.Sprintf("status code from source is %s; was expecting %s",
		strconv.Itoa(e.got), strings.Join(expected, " or "))
}

// Got is the actual status code returned by source.
func (e UnexpectedStatusCodeError) Got() int {
	return e.got
}

// CheckResponseCode returns UnexpectedStatusError if the given response code is not
// one of the allowed status codes; otherwise nil.
func CheckResponseCode(respCode int, allowed []int) error {
	for _, v := range allowed {
		if respCode == v {
			return nil
		}
	}
	return UnexpectedStatusCodeError{allowed, respCode}
}

const (
	UnknownSourceFileLen = -2
)

// ResourceClient defines the API interface to interact with source.
type ResourceClient interface {
	// GetContentLength get length of resource content
	// return source.UnknownSourceFileLen if response status is not StatusOK and StatusPartialContent
	GetContentLength(request *Request) (int64, error)

	// Download downloads from source
	Download(request *Request) (*Response, error)
}

// ResourceMetadataGetter defines the API interface to get metadata for special resource
// The metadata will be used for concurrent multiple pieces downloading
type ResourceMetadataGetter interface {
	GetMetadata(request *Request) (*Metadata, error)
}

// ResourceLister defines the interface to list all downloadable resources in request url
type ResourceLister interface {
	List(request *Request) (urls []*url.URL, err error)
}

type ClientManager interface {
	// Register registers a source client with scheme
	Register(scheme string, resourceClient ResourceClient, adapter RequestAdapter, hook ...Hook) error

	// UnRegister revoke a source client from manager
	UnRegister(scheme string)

	// GetClient gets a source client by scheme
	GetClient(scheme string, options ...Option) (ResourceClient, bool)

	// ListClients lists all supported client scheme
	ListClients() []string
}

// clientManager implements the interface ClientManager
type clientManager struct {
	mu        sync.RWMutex
	clients   map[string]ResourceClient
	pluginDir string
}

var _defaultManager = NewManager()

func NewManager() ClientManager {
	return &clientManager{
		clients: make(map[string]ResourceClient),
	}
}

type Option func(c *clientManager)

func UpdatePluginDir(pluginDir string) {
	_defaultManager.(*clientManager).pluginDir = pluginDir
}

func (m *clientManager) Register(scheme string, resourceClient ResourceClient, adaptor RequestAdapter, hooks ...Hook) error {
	scheme = strings.ToLower(scheme)
	m.mu.Lock()
	defer m.mu.Unlock()
	if client, ok := m.clients[scheme]; ok {
		if client.(*clientWrapper).rc != resourceClient {
			return fmt.Errorf("client with scheme %s already exist, current client: %#v", scheme, client)
		}
		logger.Warnf("client with scheme %s already exist, no need register again", scheme)
		return nil
	}
	m.doRegister(scheme, &clientWrapper{
		adapter: adaptor,
		hooks:   hooks,
		rc:      resourceClient,
	})
	return nil
}

func (m *clientManager) doRegister(scheme string, resourceClient ResourceClient) {
	m.clients[strings.ToLower(scheme)] = resourceClient
}

func (m *clientManager) UnRegister(scheme string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	scheme = strings.ToLower(scheme)
	if client, ok := m.clients[scheme]; ok {
		logger.Infof("remove client %#v for scheme %s", client, scheme)
	}
	delete(m.clients, scheme)
}

func (m *clientManager) ListClients() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var clients []string
	for c := range m.clients {
		clients = append(clients, c)
	}
	return clients
}

func (m *clientManager) GetClient(scheme string, options ...Option) (ResourceClient, bool) {
	logger.Debugf("current clients: %#v", m.clients)
	m.mu.RLock()
	scheme = strings.ToLower(scheme)
	client, ok := m.clients[scheme]
	if ok {
		m.mu.RUnlock()
		return client, true
	}
	m.mu.RUnlock()
	m.mu.Lock()
	client, ok = m.clients[scheme]
	if ok {
		m.mu.Unlock()
		return client, true
	}

	for _, opt := range options {
		opt(m)
	}

	client, err := LoadPlugin(m.pluginDir, scheme)
	if err != nil {
		logger.Errorf("failed to load source plugin for scheme %s: %v", scheme, err)
		m.mu.Unlock()
		return nil, false
	}
	m.doRegister(scheme, client)
	m.mu.Unlock()
	return client, true
}

func Register(scheme string, resourceClient ResourceClient, adaptor RequestAdapter, hooks ...Hook) error {
	return _defaultManager.Register(scheme, resourceClient, adaptor, hooks...)
}

func UnRegister(scheme string) {
	_defaultManager.UnRegister(scheme)
}

func ListClients() []string {
	return _defaultManager.ListClients()
}

type RequestAdapter func(request *Request) *Request

// Hook TODO hook
type Hook interface {
	BeforeRequest(request *Request) error
	AfterResponse(response *Response) error
}

type clientWrapper struct {
	adapter RequestAdapter
	hooks   []Hook
	rc      ResourceClient
}

func (c *clientWrapper) GetContentLength(request *Request) (int64, error) {
	return c.rc.GetContentLength(c.adapter(request))
}

func (c *clientWrapper) IsSupportRange(request *Request) (bool, error) {
	return c.rc.IsSupportRange(c.adapter(request))
}

func (c *clientWrapper) IsExpired(request *Request, info *ExpireInfo) (bool, error) {
	return c.rc.IsExpired(c.adapter(request), info)
}
func (c *clientWrapper) Download(request *Request) (*Response, error) {
	return c.rc.Download(c.adapter(request))
}

func (c *clientWrapper) GetLastModified(request *Request) (int64, error) {
	return c.rc.GetLastModified(c.adapter(request))
}

func GetContentLength(request *Request) (int64, error) {
	client, ok := _defaultManager.GetClient(request.URL.Scheme)
	if !ok {
		return UnknownSourceFileLen, fmt.Errorf("scheme %s: %w", request.URL.Scheme, ErrNoClientFound)
	}
	if _, ok := request.Context().Deadline(); !ok {
		ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
		request = request.WithContext(ctx)
		defer cancel()
	}
	return client.GetContentLength(request)
}

func IsSupportRange(request *Request) (bool, error) {
	client, ok := _defaultManager.GetClient(request.URL.Scheme)
	if !ok {
		return false, fmt.Errorf("scheme %s: %w", request.URL.Scheme, ErrNoClientFound)
	}
	if _, ok := request.Context().Deadline(); !ok {
		ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
		request = request.WithContext(ctx)
		defer cancel()
	}
	if request.Header.get(Range) == "" {
		request.Header.Add(Range, "0-0")
	}
	return client.IsSupportRange(request)
}

func IsExpired(request *Request, info *ExpireInfo) (bool, error) {
	client, ok := _defaultManager.GetClient(request.URL.Scheme)
	if !ok {
		return false, fmt.Errorf("scheme %s: %w", request.URL.Scheme, ErrNoClientFound)
	}
	if _, ok := request.Context().Deadline(); !ok {
		ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
		request = request.WithContext(ctx)
		defer cancel()
	}
	return client.IsExpired(request, info)
}

func GetLastModified(request *Request) (int64, error) {
	client, ok := _defaultManager.GetClient(request.URL.Scheme)
	if !ok {
		return -1, fmt.Errorf("scheme %s: %w", request.URL.Scheme, ErrNoClientFound)
	}
	if _, ok := request.Context().Deadline(); !ok {
		ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
		request = request.WithContext(ctx)
		defer cancel()
	}
	return client.GetLastModified(request)
}

func Download(request *Request) (*Response, error) {
	client, ok := _defaultManager.GetClient(request.URL.Scheme)
	if !ok {
		return nil, fmt.Errorf("scheme %s: %w", request.URL.Scheme, ErrNoClientFound)
	}
	return client.Download(request)
}

func List(request *Request) ([]*url.URL, error) {
	client, ok := _defaultManager.GetClient(request.URL.Scheme)
	if !ok {
		return nil, fmt.Errorf("scheme %s: %w", request.URL.Scheme, ErrNoClientFound)
	}
	lister, ok := client.(ResourceLister)
	if !ok {
		return nil, fmt.Errorf("scheme %s: %w", request.URL.Scheme, ErrClientNotSupportList)
	}
	return lister.List(request)
}

func GetMetadata(request *Request) (*Metadata, error) {
	client, ok := _defaultManager.GetClient(request.URL.Scheme)
	if !ok {
		return nil, fmt.Errorf("scheme %s: %w", request.URL.Scheme, ErrNoClientFound)
	}
	getter, ok := client.(*clientWrapper).rc.(ResourceMetadataGetter)
	if !ok {
		return nil, fmt.Errorf("scheme %s: %w", request.URL.Scheme, ErrClientNotSupportGetMetadata)
	}
	return getter.GetMetadata(request)
}

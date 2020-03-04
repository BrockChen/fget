package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mime"
)

var wg sync.WaitGroup

type block struct {
	targetSize int64
	index      int
}
type blockSlice []block

func (s blockSlice) Len() int           { return len(s) }
func (s blockSlice) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s blockSlice) Less(i, j int) bool { return s[i].index < s[j].index }

type Downloader struct {
	fileName      string
	uri           string
	perSize       int
	counts        int
	diff          int
	blocks        chan block
	threads       int
	proxys        Proxys
	muxGet        sync.Mutex
	muxUpdate     sync.Mutex
	downStatistic chan int
}

func fname1(uri string) string {
	u, _ := url.Parse(uri)
	n := path.Base(u.Path)
	if len(n) == 0 {
		return "default.down"
	}
	return n
}
func fname2(h http.Header) string {
	v := h.Get("content-disposition")
	if len(v) > 0 {
		_, params, err := mime.ParseMediaType(v)
		if err != nil {
			return ""
		}
		return params["filename"]
	}
	return ""
}
func NewDownloader(uri, filename string, threads, perSize int, proxys Proxys) *Downloader {
	d := &Downloader{
		uri:     uri,
		perSize: perSize * 1024 * 1024,
		//perSize:       perSize,
		blocks:        make(chan block, threads),
		downStatistic: make(chan int, 20),
		threads:       threads,
		proxys:        proxys,
	}
	d.fileName = fname1(uri)
	if len(fileName) > 0 {
		d.fileName = filename
	}
	return d
}
func (d *Downloader) getProxy() Proxy {
	d.muxGet.Lock()
	defer d.muxGet.Unlock()
	tmp := Proxy{load: 100}
	idx := 0
	for i, p := range d.proxys {
		p.idx = i
		if p.load < tmp.load {
			tmp = p
			idx = i
		}
		d.proxys[i] = p
	}
	p := d.proxys[idx]
	p.load += 1
	d.proxys[idx] = p
	return p
}
func (d *Downloader) updateLoad(idx int) {
	d.muxUpdate.Lock()
	defer d.muxUpdate.Unlock()
	for i, p := range d.proxys {
		if i == idx {
			p.load -= 1
			d.proxys[idx] = p
			break
		}
	}
}
func getHeader() http.Header {
	hd := http.Header{}
	for _, h := range headers {
		sps := strings.SplitN(h, ":", 2)
		if len(sps) > 1 {
			hd.Set(sps[0], strings.TrimSpace(sps[1]))
		}

	}
	return hd
}
func (d *Downloader) Start() blockSlice {
	lenSub := int64(d.perSize)
	h := getHeader()

	res, err := d.doReq(http.MethodGet, h)
	if err != nil {
		fmt.Println(err.Error())
		return nil
	}
	res.Body.Close()
	name := fname2(res.Header)
	if len(name) > 0 {
		d.fileName = name
	}
	fmt.Printf("status: %d\n", res.StatusCode)
	for k, v := range res.Header {
		fmt.Println(k, v[0])
	}
	maps := res.Header

	length, _ := strconv.Atoi(maps["Content-Length"][0])

	counts := length / int(lenSub)
	results := blockSlice{}
	var downSize int64 = 0
	ctx, cancle := context.WithCancel(context.Background())
	defer cancle()
	statistics := make(chan block, 1)
	cc := 0
	c := 0
	starTime := time.Now().Unix()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case s := <-statistics:
				results = append(results, s)
				//downSize += s.targetSize
			case ds := <-d.downStatistic:
				c += ds
				downSize += int64(ds)
				cc += 1
				if cc%100 == 0 {
					ti := time.Now().Unix() - starTime
					if ti > 0 {
						fmt.Printf("download progress: %d/Ks %d\n", int64(c)/(time.Now().Unix()-starTime)/1024, int64(float64(downSize)/float64(length)*100))
					}
				}

			case <-time.After(2 * time.Second):
				fmt.Println(downSize, length)
				if downSize == int64(length) {
					cancle()
					return
				}
			}

		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < d.threads; i++ {
		wg.Add(1)
		go func(tid int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					fmt.Printf("tid: %d done\n", tid)
					return
				case b := <-d.blocks:
					err := d.downBlock(b)
					if err != nil {
						fmt.Println("download error: reput", err.Error(), b)
						cancle()
						return
					} else {
						//fmt.Printf("block: %d, size: %d\n", b.index, b.targetSize)
						statistics <- b
					}
				}
			}
		}(i)
	}

	diff := int64(length) % lenSub
	if diff > 0 {
		counts += 1
	}

	for i := 0; i < counts; i++ {
		size := lenSub
		if i+1 == counts && diff > 0 {
			size = diff
		}
		select {
		case d.blocks <- block{size, i}:
			//fmt.Println("send task")
		case <-ctx.Done():
			fmt.Println("break task generator")
			break
		}
	}

	wg.Wait()
	sort.Sort(results)
	return results
}
func (d *Downloader) doReq(method string, h http.Header) (*http.Response, error) {

	u, e := url.Parse(d.uri)
	if e != nil {
		return nil, e
	}
	tr := &http.Transport{}
	if u.Scheme == "https" {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	if len(d.proxys) > 0 {
		px := d.getProxy()
		p, _ := url.Parse(px.uri)
		defer d.updateLoad(px.idx)
		tr.Proxy = http.ProxyURL(p)
	}
	client := &http.Client{Transport: tr}

	req, err := http.NewRequest(method, d.uri, nil)
	if err != nil {
		log.Println("creat new request error", err)
		return nil, err
	}

	for k, v := range h {
		req.Header.Set(k, v[0])
	}
	return client.Do(req)

}
func (d *Downloader) downBlock(b block) error {
	posStart := (d.perSize) * b.index
	posEnd := posStart + int(b.targetSize) - 1

	fname := strconv.Itoa(b.index)
	if size := d.fileSize(fname); size == b.targetSize {
		fmt.Println("cached")
		return nil
	} else if size > 0 {
		posStart += int(size)
	}

	rangeSize := fmt.Sprintf("bytes=%d-%d", posStart, posEnd)
	h := getHeader()
	h.Set("Range", rangeSize)
	resp, err := d.doReq(http.MethodGet, h)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	fIn, err := os.OpenFile(fname, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0x0777)
	if err != nil {
		return err
	}
	defer fIn.Close()
	bb := make([]byte, 1024*1024)
	for total := posEnd - posStart; total > 0; {

		n, e := resp.Body.Read(bb)
		if e != nil && e != io.EOF {
			break
		}
		_, e = fIn.Write(bb[:n])
		if e != nil {
			return e
		}
		total -= n
		d.downStatistic <- n
	}
	return nil
}

func (d *Downloader) Merge(blocks blockSlice) error {
	fIn, err := os.OpenFile(d.fileName, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0x0777)
	if err != nil {
		return err
	}
	defer fIn.Close()
	for i, b := range blocks {
		if i != b.index {
			fmt.Println("miss blocks")
		}
		fOut, err := os.Open(fmt.Sprintf("%d", b.index))
		if err != nil {
			fmt.Printf("open error: %d %s\n", b.index, err.Error())
			return err
		}

		n, err := io.Copy(fIn, fOut)
		if err != nil {
			fmt.Printf("write error: %d %s\n", b.index, err.Error())
			return err
		}
		if n != b.targetSize {
			fmt.Println("size miss match", n, int(b.targetSize))
		}

		fOut.Close()
	}
	for _, b := range blocks {
		os.Remove(fmt.Sprintf("%d", b.index))
	}
	return nil
}

func (d *Downloader) fileSize(filename string) int64 {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return -1
	}
	return info.Size()
}

type Proxy struct {
	idx  int
	uri  string
	load int
}
type Proxys []Proxy

func (i *Proxys) String() string {
	return "my string representation"
}
func (i *Proxys) Set(value string) error {
	*i = append(*i, Proxy{uri: value, load: 0})
	return nil
}

type Headers []string

func (i *Headers) String() string {
	return "my string representation"
}
func (i *Headers) Set(value string) error {
	*i = append(*i, value)
	return nil
}

var downUrl string
var proxys Proxys
var threads int
var perSize int
var fileName string
var headers Headers

func Init() {
	flag.StringVar(&downUrl, "uri", "", "download url")
	flag.Var(&proxys, "proxy", "proxy url eg: http://192.168.1.2:8080")
	flag.IntVar(&threads, "th", 10, "multi thread counts")
	flag.IntVar(&perSize, "ps", 10, "block size")
	flag.StringVar(&fileName, "o", "", "out put file name")
	flag.Var(&headers, "H", "http header")
}
func main() {
	Init()
	flag.Parse()
	fmt.Println(downUrl, fileName, threads, perSize)
	down := NewDownloader(downUrl, fileName, threads, perSize, proxys)
	r := down.Start()
	e := down.Merge(r)
	if e != nil {
		fmt.Println("", e)
	}

}

package main

import (
	"bytes"
	"flag"
	"fmt"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/proxy"
	"gopkg.in/yaml.v2"
	"html"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

type RssItem struct {
	Title       string `yaml:"title"`
	Link        string `yaml:"link"`
	Description string `yaml:"description"`
	Date        string `yaml:"date"`
	DateUnix    int64  `yaml:"date_unix"`
}

type RssChannel struct {
	Title       string    `yaml:"title"`
	Description string    `yaml:"description"`
	Link        string    `yaml:"link"`
	Items       []RssItem `yaml:"items"`
	cfgFile     string
	name        string
	xml         string
	linkSet     map[string]bool
	rwm         sync.RWMutex
}

var reTitle *regexp.Regexp
var httpCli *http.Client

func NewRssItem(url string) (*RssItem, error) {
	resp, err := httpCli.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if reTitle == nil {
		reTitle = regexp.MustCompile("(?:<html(?s:.)*<head(?s:.)*<title>)(.*)(?:</title>)")
	}
	_title := reTitle.FindStringSubmatch(string(body))
	if _title == nil {
		return nil, fmt.Errorf("No html title found")
	}
	title := html.UnescapeString(_title[1])

	description := "None"
	date := time.Now()
	return &RssItem{
		Title:       title,
		Link:        url,
		Description: description,
		Date:        date.Format(time.RFC822Z),
		DateUnix:    date.Unix(),
	}, nil
}

func (item *RssItem) SerializeXML() string {

	return fmt.Sprintf(` <item>
  <title>%s</title>
  <description>%s</description>
  <link>%s</link>
  <pubDate>%s</pubDate>
 </item>
`, item.Title, item.Description, item.Link, item.Date)
}

func LoadRssChannel(cfgFile string) (*RssChannel, error) {
	channelYaml, err := ioutil.ReadFile(cfgFile)
	if err != nil {
		return nil, err
	}
	channel := RssChannel{}
	if err = yaml.Unmarshal(channelYaml, &channel); err != nil {
		return nil, err
	}

	channel.cfgFile = cfgFile
	channel.name = strings.Split(path.Base(cfgFile), ".")[0]
	channel.xml = channel.SerializeXML()
	channel.linkSet = make(map[string]bool)
	for _, item := range channel.Items {
		channel.linkSet[item.Link] = true
	}
	return &channel, nil
}

func (channel *RssChannel) SerializeXML() string {
	var xml bytes.Buffer
	date := time.Now().Format(time.RFC822Z)
	fmt.Fprintf(&xml, `<?xml version="1.0" encoding="UTF-8" ?>
<rss version="2.0">
<channel>
 <title>%s</title>
 <description>%s</description>
 <link>%s</link>
 <lastBuildDate>%s</lastBuildDate>
 <pubDate>%s</pubDate>

`, channel.Title, channel.Description, channel.Link, date, date)

	for i := len(channel.Items) - 1; i >= 0; i-- {
		item := channel.Items[i]
		xml.WriteString(item.SerializeXML())
	}
	xml.WriteString(`</channel>
</rss>`)
	return xml.String()
}

func (channel *RssChannel) Store() error {
	channelYaml, err := yaml.Marshal(channel)
	if err != nil {
		return err
	}
	channelYaml = append(channelYaml, byte('\n'))
	ioutil.WriteFile(channel.cfgFile, channelYaml, 0644)
	return nil
}

func (channel *RssChannel) StoreAppend(item *RssItem) error {
	items := []*RssItem{item}
	itemYaml, err := yaml.Marshal(&items)
	if err != nil {
		return err
	}
	//lines := strings.Split(string(itemYaml), "\n")
	//indentLines := make([]string, len(lines), len(lines))
	//for i, line := range lines {
	//	indentLines[i] = fmt.Sprintf("  %s", line)
	//}

	f, err := os.OpenFile(channel.cfgFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	//if _, err := f.Write([]byte(strings.Join(indentLines, "\n"))); err != nil {
	if _, err := f.Write(itemYaml); err != nil {
		return err
	}

	return nil
}

func (channel *RssChannel) AddItem(item *RssItem) error {
	channel.Items = append(channel.Items, *item)
	var err error
	if len(channel.Items) == 1 {
		err = channel.Store()
	} else {
		err = channel.StoreAppend(item)
	}
	if err != nil {
		return err
	}

	channel.xml = channel.SerializeXML()
	channel.linkSet[item.Link] = true
	return nil
}

func (channel *RssChannel) AddItemByUrl(url string) (*RssItem, error) {
	if channel.linkSet[url] {
		return nil, fmt.Errorf("URL already exists in the channel")
	}
	item, err := NewRssItem(url)
	if err != nil {
		return nil, err
	}
	err = channel.AddItem(item)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func main() {
	var assetsDir, dataDir, prefix string
	var port int64
	var torify bool
	flag.StringVar(&assetsDir, "assets", "", "Assets directory")
	flag.StringVar(&dataDir, "data", "", "Data directory")
	flag.StringVar(&prefix, "prefix", "", "URL HTTP server prefix")
	flag.Int64Var(&port, "port", 8080, "Server port")
	flag.BoolVar(&torify, "torify", false, "Torify HTTP client requests")
	flag.Parse()

	if assetsDir == "" || dataDir == "" {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		return
	}

	if torify {
		dialer, err := proxy.SOCKS5("tcp", "127.0.0.1:9050", nil, proxy.Direct)
		if err != nil {
			log.Fatal(err)
		}
		httpTransport := &http.Transport{}
		httpCli = &http.Client{Transport: httpTransport}
		httpTransport.Dial = dialer.Dial
	} else {
		httpCli = http.DefaultClient
	}

	log.Println("Reading configuration...")
	rssChannels := make(map[string]*RssChannel)
	cfgFiles, err := ioutil.ReadDir(dataDir)
	if err != nil {
		log.Fatal(err)
	}
	for _, cfgFile := range cfgFiles {
		ext := path.Ext(cfgFile.Name())
		if (ext == ".yaml" || ext == ".yml") && !cfgFile.IsDir() {
			channel, err := LoadRssChannel(path.Join(dataDir, cfgFile.Name()))
			if err != nil {
				log.Fatal(err)
			}
			rssChannels[channel.name] = channel
			log.Println("Loaded config file", cfgFile.Name(), "with name:", channel.name)
		}
	}

	router := gin.Default()

	router.LoadHTMLGlob(path.Join(assetsDir, "templates", "*"))
	router.Static(fmt.Sprintf("%s/%s", prefix, "static"), path.Join(assetsDir, "static"))

	router.GET(fmt.Sprintf("%s/%s", prefix, "add/:name"), func(c *gin.Context) {
		name := c.Param("name")
		if _, ok := rssChannels[name]; !ok {
			c.String(http.StatusBadRequest, fmt.Sprintf("Channel %s not found", name))
			return
		}
		c.HTML(http.StatusOK, "add_get.html", gin.H{
			"prefix": prefix,
			"name":   name,
		})
	})

	router.POST(fmt.Sprintf("%s/%s", prefix, "add/:name"), func(c *gin.Context) {
		name := c.Param("name")
		url := c.PostForm("url")
		channel, ok := rssChannels[name]
		if !ok {
			c.String(http.StatusBadRequest, fmt.Sprintf("Channel %s not found", name))
			return
		}
		channel.rwm.Lock()
		item, err := channel.AddItemByUrl(url)
		channel.rwm.Unlock()
		if err != nil {
			c.HTML(http.StatusOK, "add_post_err.html", gin.H{
				"prefix": prefix,
				"name":   name,
				"url":    url,
				"err":    err.Error(),
			})
			return
		}
		c.HTML(http.StatusOK, "add_post_ok.html", gin.H{
			"prefix": prefix,
			"name":   name,
			"url":    url,
			"title":  item.Title,
		})
	})

	router.GET(fmt.Sprintf("%s/%s", prefix, "feed/:name"), func(c *gin.Context) {
		name := c.Param("name")
		channel, ok := rssChannels[name]
		if !ok {
			c.String(http.StatusBadRequest, fmt.Sprintf("Channel %s not found", name))
			return
		}
		channel.rwm.Lock()
		c.Data(http.StatusOK, "application/rss+xml", []byte(channel.xml))
		channel.rwm.Unlock()
	})

	router.Run(fmt.Sprintf(":%d", port))
}

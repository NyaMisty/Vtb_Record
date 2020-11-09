package stealth

import (
	"context"
	"crypto/tls"
	"github.com/fzxiao233/Vtb_Record/config"
	"github.com/fzxiao233/Vtb_Record/utils"
	log "github.com/sirupsen/logrus"
	re "github.com/umisama/go-regexpcache"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func SetupLoadBalance() {
	http.DefaultTransport = &http.Transport{
		DisableKeepAlives:  true, // disable keep alive to avoid connection reset
		DisableCompression: true,
		IdleConnTimeout:    time.Second * 20,
		ForceAttemptHTTP2:  false,
		TLSClientConfig:    &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, addr string) (conn net.Conn, err error) {
			advSettings := config.Config.AdvancedSettings
			/*addrparts := strings.SplitN(addr, ":", 2)
			if domains, ok := config.Config.DomainRewrite[addrparts[0]]; ok {
				addr = utils.RandChooseStr(domains) + ":" + addrparts[1]
			}*/
			_addr := addr
			if domains, ok := advSettings.DomainRewrite[addr]; ok {
				addr = utils.RandChooseStr(domains)
				log.Debugf("Overrided %s to %s", _addr, addr)
			}

			if advSettings.LoadBalance != nil && len(advSettings.LoadBalance) > 0 {
				needLB := true // do we need to load balance? we do it in a opt-out fashion
				if _, err := strconv.Atoi(addr[0:1]); err == nil {
					// is it an IP Address?
					needLB = false
				}

				var outIp string
				blacklisted := false
				for _, c := range advSettings.LoadBalanceBlacklist {
					if strings.Contains(addr, c) {
						blacklisted = true
					} else if matched, _ := re.MatchString(c, addr); matched {
						blacklisted = true
					}
				}
				if blacklisted {
					outIp = advSettings.LoadBalance[0]
					addr = _addr // revert to original ip
				} else if needLB {
					outIp = utils.RandChooseStr(advSettings.LoadBalance)
				} else {
					outIp = ""
				}
				if outIp != "" {
					return (&net.Dialer{
						Timeout:   30 * time.Second,
						KeepAlive: 30 * time.Second,
						LocalAddr: &net.TCPAddr{
							IP:   net.ParseIP(outIp),
							Port: 0,
						},
					}).DialContext(ctx, network, addr)
				}
			}
			return net.Dial(network, addr)
		},
	}
	if false {
		dialer := &net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		addrReplace := func(addr string) string {
			if addr == "www.googleapis.com:443" {
				//addr = "216.58.198.206:443"
				addrs := []string{"private.googleapis.com:443", "10.224.1.3:19999", "10.224.1.3:19999"}
				//addrs := []string{"10.224.1.3:19999"}
				addr = addrs[rand.Intn(len(addrs))]
			}
			return addr
		}
		dialTls :=
			func(network, addr string) (conn net.Conn, err error) {
				addr = addrReplace(addr)
				if !strings.HasSuffix(addr, ":443") {
					return dialer.Dial(network, addr)
				}
				c, err := tls.Dial(network, addr, &tls.Config{InsecureSkipVerify: true})
				if err != nil {
					//log.Println("DialTls Err:", err)
					return nil, err
				}
				//log.Println("doing handshake")
				err = c.Handshake()
				if err != nil {
					return c, err
				}
				//log.Println(c.RemoteAddr())
				return c, c.Handshake()
			}
		//dialTls := nil
		http.DefaultTransport = &http.Transport{
			DisableKeepAlives:  true, // disable keep alive to avoid connection reset
			DisableCompression: true,
			IdleConnTimeout:    time.Second * 10,
			ForceAttemptHTTP2:  false,
			DialTLS:            dialTls,
			TLSClientConfig:    &tls.Config{InsecureSkipVerify: true},
			//DialContext: func(ctx context.Context, network, addr string) (conn net.Conn, err error) {
			/*ipaddr := "10.168.1." + strconv.Itoa(100 + rand.Intn(20))
			netaddr, _ := net.ResolveIPAddr("ip", ipaddr)
			return (&net.Dialer{
				LocalAddr: &net.TCPAddr{
					IP: netaddr.IP,
				},
				Timeout:   8 * time.Second,
			}).DialContext(ctx, network, addr)*/
			/*if addr == "www.googleapis.com:443" {
				//addr = "216.58.198.206:443"
				addrs := []string{"private.googleapis.com", "www.googleapis.com:443"}
				rand.Intn(len(addrs))
				addr = "216.58.198.206:443"
			}
			return dialer.DialContext(ctx, network, addr)*/
			//},
			//ForceAttemptHTTP2:      true,
		}
	}
	/*http.DefaultTransport = &http3.RoundTripper{
		QuicConfig: &quic.Config{
			MaxIdleTimeout:        time.Second * 20,
			MaxIncomingStreams:    0,
			MaxIncomingUniStreams: 0,
			StatelessResetKey:     nil,
			KeepAlive:             false,
		},
	}*/
	http.DefaultClient.Transport = http.DefaultTransport
}

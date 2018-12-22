package main

import (
		"encoding/json"
		"errors"
		"fmt"
		"github.com/shirou/gopsutil/net"
		log "github.com/sirupsen/logrus"
		"github.com/prometheus/client_golang/prometheus"
		"github.com/prometheus/client_golang/prometheus/promhttp"
		"net/http"		
		"time"
)

//	Define the metrics we wish to expose
var netIndicator = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "sreagent_net",
		Help: "Net Stats",
	}, []string{"net","measure","operation"} )

//	Define the metrics we wish to expose
var netRates = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "sreagent_net_rates",
		Help: "Net Throughput",
	}, []string{"netif", "unit", "direction"} )



var PluginConfig 	map[string]map[string]map[string]interface{}

var PluginData,
    PluginDataPrev 	map[string]interface{}
var TScurrent,
	TSprevious 		int64

var PDropRecv,
    PDropSent		float64

func PluginMeasure() ([]byte, []byte, float64) {
	// Get measurement of IOCounters
	TScurrent	 = time.Now().UnixNano()
	netio, _ 	:= net.IOCounters(true)
	for netidx  := range netio {	PluginData[netio[netidx].Name] = netio[netidx] }

	Δts := TScurrent - TSprevious	// nanoseconds!
	PDropRecv = 0.0
	PDropSent = 0.0
	NETS:
	for netid, _ := range PluginData {
		_, present := PluginDataPrev[netid]
		if !present {continue NETS}
		inc_precv		:= PluginData[netid].(net.IOCountersStat).PacketsRecv  - PluginDataPrev[netid].(net.IOCountersStat).PacketsRecv
		inc_psent		:= PluginData[netid].(net.IOCountersStat).PacketsSent  - PluginDataPrev[netid].(net.IOCountersStat).PacketsSent
		inc_brecv		:= PluginData[netid].(net.IOCountersStat).BytesRecv    - PluginDataPrev[netid].(net.IOCountersStat).BytesRecv
		inc_bsent		:= PluginData[netid].(net.IOCountersStat).BytesSent    - PluginDataPrev[netid].(net.IOCountersStat).BytesSent

		inc_ein 		:= PluginData[netid].(net.IOCountersStat).Errin        - PluginDataPrev[netid].(net.IOCountersStat).Errin
		inc_eout		:= PluginData[netid].(net.IOCountersStat).Errout       - PluginDataPrev[netid].(net.IOCountersStat).Errout
		inc_din 		:= PluginData[netid].(net.IOCountersStat).Dropin       - PluginDataPrev[netid].(net.IOCountersStat).Dropin
		inc_dout		:= PluginData[netid].(net.IOCountersStat).Dropout      - PluginDataPrev[netid].(net.IOCountersStat).Dropout

		// Update metrics related to the plugin
		netIndicator.WithLabelValues(netid, "packets", "received").Add(float64(inc_precv))
		netIndicator.WithLabelValues(netid, "packets", "sent"    ).Add(float64(inc_psent))
		netIndicator.WithLabelValues(netid, "bytes",   "received").Add(float64(inc_brecv))
		netIndicator.WithLabelValues(netid, "bytes",   "sent"    ).Add(float64(inc_bsent))
		netIndicator.WithLabelValues(netid, "errors",  "received").Add(float64(inc_ein))
		netIndicator.WithLabelValues(netid, "errors",  "sent"    ).Add(float64(inc_eout))
		netIndicator.WithLabelValues(netid, "packets", "dropin"  ).Add(float64(inc_din))
		netIndicator.WithLabelValues(netid, "packets", "dropout" ).Add(float64(inc_dout))

		ppsrecv := float64(inc_precv) * 1e9 / float64(Δts)
		ppssent := float64(inc_psent) * 1e9 / float64(Δts)

		droprecv := float64(inc_din)  * 1e9 / float64(Δts)
		dropsent := float64(inc_dout) * 1e9 / float64(Δts)

		bpsrecv := float64(8 * inc_brecv) * 1e9 / (float64(Δts) * 1024.0 * 1024.0)
		bpssent := float64(8 * inc_bsent) * 1e9 / (float64(Δts) * 1024.0 * 1024.0)

		netRates.WithLabelValues(netid, "pps",    "received" ).Set(ppsrecv)
		netRates.WithLabelValues(netid, "pps",    "sent"     ).Set(ppssent)
		netRates.WithLabelValues(netid, "dropps", "received" ).Set(droprecv)
		netRates.WithLabelValues(netid, "dropps", "sent"     ).Set(dropsent)
		netRates.WithLabelValues(netid, "mbps",   "received" ).Set(bpsrecv)
		netRates.WithLabelValues(netid, "mbps",   "sent"     ).Set(bpssent)

		PDropRecv += droprecv
		PDropSent += dropsent
	}
	// save current values as previous
	for netid, _ := range PluginData {
		_, present := PluginDataPrev[netid]
		if present { PluginDataPrev[netid] = PluginData[netid] }
	}
	TSprevious    = TScurrent
	myMeasure, _ := json.Marshal(PluginData)
	return myMeasure, []byte(""), float64(TScurrent) / 1e9
}

func PluginAlert(measure []byte) (string, string, bool, error) {
	// log.WithFields(log.Fields{"MyMeasure": string(MyMeasure[:]), "measure": string(measure[:])}).Info("PluginAlert")
	// var m 			interface{}
	// err := json.Unmarshal(measure, &m)
	// if err != nil { return "unknown", "", true, err }
	alertMsg := ""
	alertLvl := ""
	alertFlag := false
	alertErr := errors.New("no error")
	// Check that the Dropped rates are good
	switch {
		case PDropSent > PluginConfig["alert"]["drop"]["engineered"].(float64):
			alertLvl  = "fatal"
			alertMsg  += "Overall Packet Drop sent above engineered point "
			alertFlag = true
			alertErr  = errors.New("excessive drop in sending")
			// return now, looks bad
			return alertMsg, alertLvl, alertFlag, alertErr
		case PDropRecv > PluginConfig["alert"]["drop"]["engineered"].(float64):
			alertLvl  = "fatal"
			alertMsg  += "Overall Packet Drop recv above engineered point "
			alertFlag = true
			alertErr  = errors.New("excessive drop in receiving")
			// return now, looks bad
			return alertMsg, alertLvl, alertFlag, alertErr
		case PDropSent > PluginConfig["alert"]["drop"]["design"].(float64):
			alertLvl  = "warn"
			alertMsg  += "Overall Packet Drop sent above design point "
			alertFlag = true
			alertErr  = errors.New("moderately high packet drop sent")
		case PDropRecv > PluginConfig["alert"]["drop"]["design"].(float64):
			alertLvl  = "warn"
			alertMsg  += "Overall Packet Drop recv above design point "
			alertFlag = true
			alertErr  = errors.New("moderately high packet drop recv")
	}
	return alertMsg, alertLvl, alertFlag, alertErr
}

func InitPlugin(config string) {
	if PluginData == nil {
		PluginData = make(map[string]interface{}, 20)
	}
	if PluginDataPrev == nil {
		PluginDataPrev = make(map[string]interface{}, 20)
	}
	if PluginConfig == nil {
		PluginConfig = make(map[string]map[string]map[string]interface{}, 20)
	}
	err := json.Unmarshal([]byte(config), &PluginConfig)
	if err != nil {
		log.WithFields(log.Fields{"config": config}).Error("failed to unmarshal config")
	}

	PDropRecv = 0.0
	PDropSent = 0.0
	TSprevious 	= time.Now().UnixNano()
	netio, _ 	:= net.IOCounters(true)
	for netidx := range netio {	PluginDataPrev[netio[netidx].Name] = netio[netidx] }
	// Register metrics with prometheus
	prometheus.MustRegister(netIndicator)
	prometheus.MustRegister(netRates)

	log.WithFields(log.Fields{"pluginconfig": PluginConfig, "plugindata": PluginData}).Info("InitPlugin")
}

func main() {
	config  := 	`
				{
					"alert": 
					{
						"drop":
						{
							"low": 			0.00,
							"design": 		1.0,
							"engineered":	10.0
						}
				    }
				}
				`

	//--------------------------------------------------------------------------//
	// time to start a prometheus metrics server
	// and export any metrics on the /metrics endpoint.
	http.Handle("/metrics", promhttp.Handler())
	go func() {
		http.ListenAndServe(":8999", nil)
	}()
	//--------------------------------------------------------------------------//

	InitPlugin(config)
	log.WithFields(log.Fields{"PluginConfig": PluginConfig}).Info("InitPlugin")
	tickd := 200 * time.Millisecond
	for i := 1; i <= 100; i++ {
		tick := time.Now().UnixNano()
		measure, measureraw, measuretimestamp := PluginMeasure()
		alertmsg, alertlvl, isAlert, err := PluginAlert(measure)
		fmt.Printf("Iteration #%d tick %d %v \n", i, tick,PluginData)
		log.WithFields(log.Fields{"timestamp": measuretimestamp,
			"measure":    string(measure[:]),
			"measureraw": string(measureraw[:]),
			"PluginData": PluginData,
			"alertMsg":   alertmsg,
			"alertLvl":   alertlvl,
			"isAlert":    isAlert,
			"AlertErr":   err,
		}).Debug("Tick")
		time.Sleep(tickd)
	}
}

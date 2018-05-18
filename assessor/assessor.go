package main

import (
	"goproxy/library"
	"log"
	"math"
	"strings"
	"time"

	"github.com/parnurzeal/gorequest"
	"github.com/robfig/cron"
)

type Assessor struct {
	Config    *library.Config
	Queue     []*library.ValidProxy
	MySQL     *library.MySQL
	Scheduler *cron.Cron
	Count     int
}

/**
Set 4 impact factors, namely AssessTimes, SuccessTimes, Speed, Mutation
Continuously increasing Mutation value will lead to a sharp drop in Score
Formula affected by SuccessRate and AssessTimes at the same time.
Formulas can be derived by yourself
*/
func GetScore(p *library.ValidProxy) float64 {
	times := float64(p.AssessTimes)
	success := float64(p.SuccessTimes)
	speed := math.Sqrt(float64(library.ProxyAssessTimeOut)) / p.AvgResponseTime
	mutation := 1 / math.Pow(float64(p.ContinuousFailedTimes)+1, 2.0)
	return success * speed * mutation / math.Sqrt(times)
}

func (s *Assessor) ProxyAssess(p *library.ValidProxy) {
	var r library.HTTPbinIP
	request := gorequest.New().Proxy(p.Content).Timeout(library.ProxyAssessTimeOut * time.Second)
	timeStart := time.Now().UnixNano() / 1e6
	resp, _, errs := request.Get("http://httpbin.org/ip").
		EndStruct(&r)
	if len(errs) == 0 && resp.StatusCode == 200 && strings.Contains(p.Content, r.Origin) {
		timeCost := time.Now().UnixNano()/1e6 - timeStart
		s.ProxyAssessFeedBack(p, 1, float64(timeCost)/1e3)
		log.Printf("Pass Assess(%dms): %s", timeCost, p.Content)
	} else {
		log.Printf("Fail Assess: %s", p.Content)
		s.ProxyAssessFeedBack(p, 0, float64(library.ProxyAssessTimeOut)*1.5)
	}
	return
}

func (s *Assessor) ProxyAssessFeedBack(p *library.ValidProxy, isSucc int, responseTime float64) {
	p.AssessTimes++
	times := float64(p.AssessTimes)
	p.AvgResponseTime = (p.AvgResponseTime*(times-1.0) + responseTime) / times

	if isSucc == 1 {
		p.ContinuousFailedTimes = 0
	} else {
		p.ContinuousFailedTimes++
	}
	p.SuccessTimes += isSucc
	p.LastAssessTime = time.Now().Unix()
	p.Score = GetScore(p)
	s.UpdateValidProxy(p)
}

func (s *Assessor) UpdateValidProxy(p *library.ValidProxy) {
	succRate := float64(p.SuccessTimes) / float64(p.AssessTimes)
	if succRate < library.AllowProxyAssessSuccessRateMin {
		s.MySQL.Connection.Delete(p)
	} else {
		s.MySQL.Connection.Save(p)
	}
}

func (s *Assessor) GetValidProxy() {
	length := library.ProxyAssessQueueMin - len(s.Queue)
	//log.Printf("QueueLength:%d, Dert:%d", len(s.Queue), length)
	if length > 0 {
		s.MySQL.Connection.
			Where("unix_timestamp(now()) - last_assess_time >= ?", library.ProxyAssessInterval).
			Or("last_assess_time = ?", 0).
			Order("last_assess_time").
			Limit(length).
			Find(&s.Queue)
		// log.Printf("Maybe has fetched %d from database, current queue length: %d", len(s.Queue)-length, len(s.Queue))
	}
}

func (s *Assessor) Start() {
	s.GetValidProxy()
	s.Scheduler.AddFunc("@every 3s", func() {
		s.GetValidProxy()
	})
	s.Scheduler.Start()
	s.Count = 0
	var p *library.ValidProxy
	for {
		time.Sleep(50 * time.Millisecond)
		if len(s.Queue) > 0 {
			s.Count++
			p, s.Queue = s.Queue[0], s.Queue[1:]
			go s.ProxyAssess(p)
		}
		p = nil
	}
}

func NewAssessor() *Assessor {
	c := library.GetConfig()
	d := library.GetMysqlDsn(c)
	return &Assessor{
		Config:    c,
		MySQL:     library.NewMySQL(d),
		Scheduler: cron.New(),
	}
}

func main() {
	// log.SetFlags(log.Lshortfile | log.LstdFlags)
	assessor := NewAssessor()
	assessor.Start()
}
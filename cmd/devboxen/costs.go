package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/dx-tooling/devboxen/internal/config"
)

// What an on-demand host costs, from AWS Cost Explorer, for the AWS account the host is in.
// devboxen assumes that account holds only this host (a dedicated account is the
// recommended setup), so the account's costs are the host's.
//
// Every Cost Explorer call costs $0.01, and AWS updates its figures a few times a day with a
// lag of up to a day. So they are cached for 6 hours; one fetch is two calls (the daily
// costs and the month-end forecast). For a running host, the current run is also estimated
// from its start time and the instance's on-demand price (the free Price List API, cached):
// no lag, but the instance only, not disks or address.

const costTTL = 6 * time.Hour

type costCache struct {
	Day       string             `json:"day"` // the UTC day of the fetch
	FetchedAt time.Time          `json:"fetched_at"`
	Forecast  *float64           `json:"forecast"` // rest of the month, if AWS has one
	Days      map[string]float64 `json:"days"`     // UTC date -> USD
}

type hostCosts struct {
	Status                 string // ok, login, error
	Today, Yesterday, Week float64
	Month                  float64
	MonthEnd               *float64
	FetchedAt              time.Time
	Days                   []float64 // the last 7 days, oldest first
	Running                time.Duration
	RunCost, Hourly        *float64
	Type                   string
	Known                  bool // any figures at all
}

func cacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "devboxen")
}

func fetchCosts(h *config.Host, login bool) (*costCache, error) {
	run := awsRun
	if login {
		run = awsDo
	}
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	start := now.AddDate(0, 0, -6)
	if month.Before(start) {
		start = month
	}
	out, err := run(h, "us-east-1", "ce", "get-cost-and-usage",
		"--time-period", "Start="+start.Format("2006-01-02")+",End="+now.AddDate(0, 0, 1).Format("2006-01-02"),
		"--granularity", "DAILY", "--metrics", "UnblendedCost", "--output", "json")
	if err != nil {
		return nil, err
	}
	var res struct {
		ResultsByTime []struct {
			TimePeriod struct{ Start string }
			Total      map[string]struct{ Amount string }
		}
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, err
	}
	c := &costCache{Day: today, FetchedAt: time.Now(), Days: map[string]float64{}}
	for _, r := range res.ResultsByTime {
		v, _ := strconv.ParseFloat(r.Total["UnblendedCost"].Amount, 64)
		c.Days[r.TimePeriod.Start] = v
	}
	// A young account has no forecast yet ("Insufficient amount of historical data").
	nextMonth := month.AddDate(0, 1, 0).Format("2006-01-02")
	if fc, err := run(h, "us-east-1", "ce", "get-cost-forecast",
		"--time-period", "Start="+today+",End="+nextMonth, "--granularity", "MONTHLY",
		"--metric", "UNBLENDED_COST", "--query", "Total.Amount", "--output", "text"); err == nil {
		if v, err := strconv.ParseFloat(fc, 64); err == nil {
			c.Forecast = &v
		}
	}
	return c, nil
}

// hourlyPrice: the on-demand Linux price of an instance type in the host's region (cached 30 days).
func hourlyPrice(h *config.Host, typ string) (float64, error) {
	path := filepath.Join(cacheDir(), "price-"+h.AWS.Region+"-"+typ)
	if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < 30*24*time.Hour {
		if b, err := os.ReadFile(path); err == nil {
			if v, err := strconv.ParseFloat(string(b), 64); err == nil {
				return v, nil
			}
		}
	}
	out, err := awsRun(h, "us-east-1", "pricing", "get-products", "--service-code", "AmazonEC2", "--output", "json",
		"--filters",
		"Type=TERM_MATCH,Field=instanceType,Value="+typ,
		"Type=TERM_MATCH,Field=regionCode,Value="+h.AWS.Region,
		"Type=TERM_MATCH,Field=operatingSystem,Value=Linux",
		"Type=TERM_MATCH,Field=tenancy,Value=Shared",
		"Type=TERM_MATCH,Field=preInstalledSw,Value=NA",
		"Type=TERM_MATCH,Field=capacitystatus,Value=Used",
		"Type=TERM_MATCH,Field=licenseModel,Value=No License required")
	if err != nil {
		return 0, err
	}
	var res struct{ PriceList []string }
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.PriceList) == 0 {
		return 0, errors.New("no price found")
	}
	var item struct {
		Terms struct {
			OnDemand map[string]struct {
				PriceDimensions map[string]struct {
					PricePerUnit map[string]string
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(res.PriceList[0]), &item); err != nil {
		return 0, err
	}
	for _, term := range item.Terms.OnDemand {
		for _, dim := range term.PriceDimensions {
			if v, err := strconv.ParseFloat(dim.PricePerUnit["USD"], 64); err == nil {
				_ = os.MkdirAll(cacheDir(), 0o755)
				_ = os.WriteFile(path, []byte(strconv.FormatFloat(v, 'f', -1, 64)), 0o644)
				return v, nil
			}
		}
	}
	return 0, errors.New("no price found")
}

// costsOf: the host's costs, from the cache if it is fresh (refresh: fetch anyway). login:
// renew an expired AWS login (else the status says "login").
func costsOf(h *config.Host, refresh, login bool) hostCosts {
	r := hostCosts{Status: "ok"}
	path := filepath.Join(cacheDir(), "costs-"+h.Name+".json")
	var c *costCache
	if b, err := os.ReadFile(path); err == nil {
		c = &costCache{}
		if json.Unmarshal(b, c) != nil {
			c = nil
		}
	}
	today := time.Now().UTC().Format("2006-01-02")
	if refresh || c == nil || time.Since(c.FetchedAt) > costTTL || c.Day != today {
		fresh, err := fetchCosts(h, login)
		switch {
		case err == nil:
			c = fresh
			_ = os.MkdirAll(cacheDir(), 0o755)
			if b, err := json.Marshal(c); err == nil {
				_ = os.WriteFile(path, b, 0o644)
			}
		case errors.Is(err, errLogin):
			r.Status = "login"
		default:
			r.Status = "error"
		}
	}
	if c != nil {
		r.Known = true
		r.FetchedAt = c.FetchedAt
		now := time.Now().UTC()
		yday := now.AddDate(0, 0, -1).Format("2006-01-02")
		week := now.AddDate(0, 0, -6).Format("2006-01-02")
		month := now.Format("2006-01") + "-01"
		var before float64
		keys := make([]string, 0, len(c.Days))
		for d, v := range c.Days {
			keys = append(keys, d)
			if d == today {
				r.Today += v
			}
			if d == yday {
				r.Yesterday += v
			}
			if d >= week {
				r.Week += v
			}
			if d >= month {
				r.Month += v
				if d < today {
					before += v
				}
			}
		}
		sort.Strings(keys)
		for _, d := range keys {
			if d >= week {
				r.Days = append(r.Days, c.Days[d])
			}
		}
		if c.Forecast != nil {
			v := before + *c.Forecast
			r.MonthEnd = &v
		}
	}
	if r.Status != "login" {
		if inst, err := describe(h, false); err == nil {
			r.Type = inst.Type
			if inst.State == "running" && !inst.Launch.IsZero() {
				r.Running = time.Since(inst.Launch)
				if p, err := hourlyPrice(h, inst.Type); err == nil {
					cost := r.Running.Hours() * p
					r.RunCost, r.Hourly = &cost, &p
				}
			}
		}
	}
	return r
}

func (a *app) cmdCosts(args []string) error {
	pos, opts, err := splitFlags(args, map[string]bool{"refresh": false})
	if err != nil {
		return err
	}
	var hosts []*config.Host
	if len(pos) > 0 {
		h, err := a.onDemandHost(pos)
		if err != nil {
			return err
		}
		hosts = append(hosts, h)
	} else {
		for _, name := range a.cfg.Order {
			if h := a.cfg.Hosts[name]; h.OnDemand() {
				hosts = append(hosts, h)
			}
		}
		if len(hosts) == 0 {
			return errors.New("no on-demand host in devboxen.toml: no costs to show")
		}
	}
	for _, h := range hosts {
		c := costsOf(h, opts["refresh"] != "", true)
		fmt.Printf("%s  (AWS account of profile %s)\n", h.Name, h.AWS.Profile)
		if c.Status != "ok" {
			fmt.Printf("  could not fetch the costs (%s); showing the last ones fetched\n", c.Status)
		}
		if !c.Known {
			fmt.Println("  no figures yet")
		} else {
			fmt.Printf("  %-16s $%.2f\n  %-16s $%.2f\n  %-16s $%.2f\n  %-16s $%.2f\n",
				"today so far", c.Today, "yesterday", c.Yesterday, "last 7 days", c.Week,
				time.Now().UTC().Format("January"), c.Month)
			if c.MonthEnd == nil {
				fmt.Printf("  %-16s no forecast yet (AWS needs more history)\n", "month end")
			} else {
				fmt.Printf("  %-16s $%.2f (forecast)\n", "month end", *c.MonthEnd)
			}
			fmt.Printf("  fetched %s; AWS's figures trail by up to a day (days are UTC)\n", c.FetchedAt.Local().Format("2006-01-02 15:04"))
		}
		if c.Running > 0 {
			fmt.Printf("  %-16s %dh%02dm", "running since", int(c.Running.Hours()), int(c.Running.Minutes())%60)
			if c.RunCost != nil {
				fmt.Printf(" ≈ $%.2f (%s at $%.4f/h; the instance only)\n", *c.RunCost, c.Type, *c.Hourly)
			} else {
				fmt.Printf(" (%s)\n", c.Type)
			}
		}
	}
	return nil
}

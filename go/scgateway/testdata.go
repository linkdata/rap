package main

import (
	"log"
	"net/http"

	_ "github.com/linkdata/rap/go"
	"github.com/naoina/denco"
)

type testStruct struct {
	Method      string
	URI         string
	TestStrings []string
	HandlerFunc denco.HandlerFunc
}

var testData = []testStruct{
	{
		"GET", "/",
		[]string{
			"/",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {
			// log.Print(r.Method, " ", r.URL, " params ", ps)
		},
	},
	{ // duplicate should error
		"GET", "/",
		[]string{
			"/",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {
		},
	},
	{
		"GET", "/uri-with-req/:p",
		[]string{
			"/uri-with-req/123",
			"/uri-with-req/KalleKula",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {
			// log.Print(r.Method, " ", r.URL, " params ", ps)
		},
	},
	{
		"GET", "/test",
		[]string{
			"/test",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/uri-with-req",
		[]string{
			"/uri-with-req",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/admin/apapapa/:p",
		[]string{
			"/admin/apapapa/19",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/admin/:p1/:p2",
		[]string{
			"/admin/KalleKula/123",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/admin/:p1",
		[]string{
			"/admin/KalleKula",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/players",
		[]string{
			"/players",
			"/players?KalleKula",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/players/:p1/abc/:p2",
		[]string{
			"/players/123/abc/John",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/dashboard/:p1",
		[]string{
			"/dashboard/123",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/players/:p1",
		[]string{
			"/players/123",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/whatever/:p1/more/:p2/:p3",
		[]string{
			"/whatever/apapapa/more/5547/KalleKula",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/ordinary",
		[]string{
			"/ordinary",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/ordanary",
		[]string{
			"/ordanary",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/aaaaa/:p1/bbbb",
		[]string{
			"/aaaaa/90510/bbbb",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/whatever/:p1/xxyx/:p2",
		[]string{
			"/whatever/abrakadabra19/xxyx/91119",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/whatever/:p1/xxzx/:p2",
		[]string{
			"/whatever/abrakadabra20/xxzx/91120",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/whatmore/:p1/xxzx/:p2",
		[]string{
			"/whatmore/abrakadabra21/xxzx/91121",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/test-decimal/:p1",
		[]string{
			"/test-decimal/99.123",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/test-double/:p1",
		[]string{
			"/test-double/99.124",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/test-bool/:p1",
		[]string{
			"/test-bool/true",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/static:p1/:p2",
		[]string{
			"/staticmarknad/nyhetsbrev",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"PUT", "/players/:p1",
		[]string{
			"/players/125",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"POST", "/transfer",
		[]string{
			"/transfer?99",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"POST", "/deposit",
		[]string{
			"/deposit?56754",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"POST", "/find-player",
		[]string{
			"/find-player?firstname=Kalle&lastname=Kula&age=19",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"DELETE", "/all",
		[]string{
			"/all",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/static/:p1/static",
		[]string{
			"/static/KvaKva/static",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/:p1/:p2",
		[]string{
			"/657567/true",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/:p1/:p2/:p3",
		[]string{
			"/1657567/false/-1.3457",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/:p1",
		[]string{
			"/-678678",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/:p1/:p2/*p3",
		[]string{
			"/-725845/Hello!/hello!!/foo",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/ab",
		[]string{
			"/ab",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/s*p1",
		[]string{
			"/sHej!",
			"/sa",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/:p1/static/:p2",
		[]string{
			"/Hej!/static/Hop!",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/:p1/static/*wildcard",
		[]string{
			"/a/static/a/b",
			"/b/static/a/b/c",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/databases/:p1",
		[]string{
			"/databases/someanother",
			"/databases/some?another",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/databases",
		[]string{
			"/databases",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"GET", "/databases/:p1/ending",
		[]string{
			"/databases/some/ending",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {
			if ps.Get("p1") != "some" {
				log.Print(ps)
				panic(r.URL)
			}
		},
	},
	{
		"GET", "/databases/:p1/else",
		[]string{
			"/databases/some2/else",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"POST", "/databases",
		[]string{
			"/databases",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
	{
		"POST", "/databases/:p1",
		[]string{
			"/databases/somestuff",
		},
		func(w http.ResponseWriter, r *http.Request, ps denco.Params) {},
	},
}

/*
func addSomeRoutes(router *scgateway.Router) {
	for k, td := range testData {
		rule, err := scgateway.NewRoute(scgateway.RoutingId(k), "", td.Method, td.Uri)
		if err != nil {
			panic(err)
		}
		err = router.Register(rule)
		if err != nil {
			panic(err)
		}
		if rt := router.ReverseLookup(rule.RoutingId); rt == nil {
			log.Fatal("can't reverse lookup %v", rule)
		} else {
			if rt.HostPort != "" {
				panic("wrong host name")
			}
			if rt.Method != td.Method {
				log.Fatal("wrong method name, expect ", td.Method, " actual ", rt.Method)
			}
			if rt.PathPattern != td.Uri {
				log.Fatal("wrong path pattern, expect ", td.Uri, " actual ", rt.PathPattern)
			}
		}
		// log.Println(rid, td.Method, td.Uri)
	}
	if err := router.Build(); err != nil {
		panic(err)
	}
}
*/

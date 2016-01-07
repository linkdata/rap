using System;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.AspNet.Builder;
using Microsoft.AspNet.Http;
using Microsoft.Framework.Logging;
using Starcounter.Rap;

namespace HelloWeb
{
    public class Startup
    {
        private Int64 _lastheadcount = 0;
        private Timer _timer = null;
        private Server _rapserver = null;
        
        public Task RequestHandler(HttpContext context)
        {
            context.Response.ContentLength = 14;
            return context.Response.WriteAsync("Hello world!\r\n");
        }

        public void Configure(IApplicationBuilder app, ILoggerFactory loggerFactory)
        {
            TimerCallback tcb = OnTimedEvent;
            _timer = new Timer(tcb, null, 1000, 1000);
            loggerFactory.AddConsole();
            _rapserver = new Server();
            _rapserver.Run();
            app.Run(RequestHandler);
        }
        
        private void OnTimedEvent(Object stateInfo)
        {
            var headcount = _rapserver.StatHeadCount;
            if (headcount != _lastheadcount)
            {
                Console.WriteLine("{0} RPS", headcount - _lastheadcount);
                _lastheadcount = headcount;
            }
        }
    }
}

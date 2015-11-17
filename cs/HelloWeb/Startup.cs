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
        private Server _rapserver = null;
        private Thread _rapthread = null;
        
        public Task RequestHandler(HttpContext context)
        {
            context.Response.ContentLength = 14;
            return context.Response.WriteAsync("Hello world!\r\n");
        }

        public void Configure(IApplicationBuilder app, ILoggerFactory loggerFactory)
        {
            loggerFactory.AddConsole();
            _rapserver = new Server();
            _rapthread = new Thread(_rapserver.Run);
            _rapthread.Start();
            app.Run(RequestHandler);
        }
    }
}

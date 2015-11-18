using System;
using System.Net;
using System.Net.Sockets;
using System.Threading;

namespace Starcounter.Rap
{
    public class Server
    {
        private readonly string _host;
        private readonly int _port;
        private Socket _listenSocket;
        private Int64 _statConnCount = 0;
        private Int64 _statReadBytes = 0;
        private Int64 _statWriteBytes = 0;
        private Int64 _statHeadCount = 0;
    
        public Server(string hostnameOrAdress = null, int port = 10111)
        {
            _host = hostnameOrAdress;
            _port = port;
        }
        
        public void StatConnCountInc()
        {
            Interlocked.Increment(ref _statConnCount);
        }

        public void StatConnCountDec()
        {
            Interlocked.Decrement(ref _statConnCount);
        }
        
        public void StatReadBytesAdd(Int64 n)
        {
            Interlocked.Add(ref _statReadBytes, n);
        }

        public void StatWriteBytesAdd(Int64 n)
        {
            Interlocked.Add(ref _statWriteBytes, n);
        }

        public void StatHeadCountInc()
        {
            Interlocked.Increment(ref _statHeadCount);
        }
        
        public Int64 StatHeadCount
        {
            get { return _statHeadCount; }
        }
        
        public Int64 StatReadBytes
        {
            get { return _statReadBytes; }
        }

        public void Run()
        {
            try {
                IPAddress ipAddress = IPAddress.Any;
                if (_host != null)
                {
                    IPHostEntry ipHostInfo = Dns.GetHostEntryAsync(_host).Result;
                    ipAddress = ipHostInfo.AddressList[0];
                }
                IPEndPoint localEndPoint = new IPEndPoint(ipAddress, _port);
                _listenSocket = new Socket(localEndPoint.AddressFamily, SocketType.Stream, ProtocolType.Tcp);
                Console.WriteLine("Server.Run(): Starting to listen {0}", localEndPoint);
                _listenSocket.Bind(localEndPoint);
                _listenSocket.Listen(5);
                StartAccept(null);
            } catch (Exception e) {
                Console.WriteLine(e.ToString());
            }
        }
        
        public void StartAccept(SocketAsyncEventArgs acceptEventArg)
        {
            if (acceptEventArg == null)
            {
                acceptEventArg = new SocketAsyncEventArgs();
                acceptEventArg.Completed += new EventHandler<SocketAsyncEventArgs>(AcceptEventArg_Completed);
            }
            else
            {
                // socket must be cleared since the context object is being reused
                acceptEventArg.AcceptSocket = null;
            }
            if (!_listenSocket.AcceptAsync(acceptEventArg))
            {
                ProcessAccept(acceptEventArg);
            }
        }
    
        void AcceptEventArg_Completed(object sender, SocketAsyncEventArgs e)
        {
            ProcessAccept(e);
        }
    
        private void ProcessAccept(SocketAsyncEventArgs e)
        {
            Console.WriteLine("ProcessAccept");
            var conn = new Conn(this, e.AcceptSocket);
            StartAccept(e);
        }
    }
}

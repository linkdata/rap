using System;
using System.Collections.Generic;
using System.Threading.Tasks;
using Microsoft.Framework.Runtime;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Threading;
using Starcounter.Rap;

public class RapEngine : IDisposable
{
    private CancellationToken _stopping;
    private Thread _thread = null;

    public RapEngine()
    {
        // _thread = new Thread(Run);
    }

#if false
    public void Run()
    {
        Server s = new Server();
        s.Run();
    }
        
        private async Task<int> HandleConn(TcpClient client)
        {
            int request_count = 0;
            byte[] rdbuf = new byte[0x10000];
            NetworkStream s = client.GetStream();
            while (!_stopping.IsCancellationRequested) {
                int nread = await s.ReadAsync(rdbuf, 0, 4, _stopping);
            }
            return request_count;
        }
        
        public async Task<int> Start(string hostnameOrAdress = null, int port = 10111)
        {
            int active_conns = 0;
            int request_count = 0;
            Interlocked.Add(ref request_count, 0);
            if (hostnameOrAdress == null)
                hostnameOrAdress = Dns.GetHostName();
            try {
                IPHostEntry ipHostInfo = await Dns.GetHostEntryAsync(hostnameOrAdress);
                IPAddress ipAddress = ipHostInfo.AddressList[0];
                var listener = new TcpListener(ipAddress, port);
                while (!_stopping.IsCancellationRequested) {
                    TcpClient conn = await listener.AcceptTcpClientAsync();
                    Interlocked.Add(ref active_conns, 1);
                    HandleConn(conn).ContinueWith((t) => {
                        Interlocked.Add(ref request_count, t.Result);
                        Interlocked.Add(ref active_conns, -1);
                        }
                    );
                }
            } catch (Exception e) {
                Console.WriteLine(e.ToString());
            }
        }

        public IDisposable CreateServer(string scheme, string host, int port, Func<Frame, Task> application)
        {
            //var listeners = new List<Listener>();

            try
            {
                /*
                foreach (var thread in Threads)
                {
                    var listener = new Listener(Memory);

                    listeners.Add(listener);
                    listener.StartAsync(scheme, host, port, thread, application).Wait();
                }
                return new Disposable(() =>
                {
                    foreach (var listener in listeners)
                    {
                        listener.Dispose();
                    }
                });
                */
            }
            catch
            {
                /*
                foreach (var listener in listeners)
                {
                    listener.Dispose();
                }
                */

                throw;
            }
        }
    }
    
    // State object for reading client data asynchronously
    public class StateObject {
        // Client  socket.
        public Socket workSocket = null;
        // Size of receive buffer.
        public const int BufferSize = 1024;
        // Receive buffer.
        public byte[] buffer = new byte[BufferSize];
    // Received data string.
        public StringBuilder sb = new StringBuilder();  
    }
    
    public class AsynchronousSocketListener {
        // Thread signal.
        public static ManualResetEvent allDone = new ManualResetEvent(false);
    
        public AsynchronousSocketListener() {
        }
    
        public static void StartListening() {
            // Data buffer for incoming data.
            byte[] bytes = new Byte[1024];
    
            // Establish the local endpoint for the socket.
            // The DNS name of the computer
            // running the listener is "host.contoso.com".
            IPHostEntry ipHostInfo = Dns.GetHostEntryAsync(Dns.GetHostName()).Result;
            IPAddress ipAddress = ipHostInfo.AddressList[0];
            IPEndPoint localEndPoint = new IPEndPoint(ipAddress, 11000);
    
            // Create a TCP/IP socket.
            Socket listener = new Socket(AddressFamily.InterNetwork,
                SocketType.Stream, ProtocolType.Tcp );
    
            // Bind the socket to the local endpoint and listen for incoming connections.
            try {
                listener.Bind(localEndPoint);
                listener.Listen(100);
    
                while (true) {
                    // Set the event to nonsignaled state.
                    allDone.Reset();
    
                    // Start an asynchronous socket to listen for connections.
                    Console.WriteLine("Waiting for a connection...");
                    listener.BeginAccept( 
                        new AsyncCallback(AcceptCallback),
                        listener );
    
                    // Wait until a connection is made before continuing.
                    allDone.WaitOne();
                }
    
            } catch (Exception e) {
                Console.WriteLine(e.ToString());
            }
    
            Console.WriteLine("\nPress ENTER to continue...");
            Console.Read();
    
        }
    
        public static void AcceptCallback(IAsyncResult ar) {
            // Signal the main thread to continue.
            allDone.Set();
    
            // Get the socket that handles the client request.
            Socket listener = (Socket) ar.AsyncState;
            Socket handler = listener.EndAccept(ar);
    
            // Create the state object.
            StateObject state = new StateObject();
            state.workSocket = handler;
            handler.BeginReceive( state.buffer, 0, StateObject.BufferSize, 0,
                new AsyncCallback(ReadCallback), state);
        }
    
        public static void ReadCallback(IAsyncResult ar) {
            String content = String.Empty;
    
            // Retrieve the state object and the handler socket
            // from the asynchronous state object.
            StateObject state = (StateObject) ar.AsyncState;
            Socket handler = state.workSocket;
    
            // Read data from the client socket. 
            int bytesRead = handler.EndReceive(ar);
    
            if (bytesRead > 0) {
                // There  might be more data, so store the data received so far.
                state.sb.Append(Encoding.ASCII.GetString(
                    state.buffer,0,bytesRead));
    
                // Check for end-of-file tag. If it is not there, read 
                // more data.
                content = state.sb.ToString();
                if (content.IndexOf("<EOF>") > -1) {
                    // All the data has been read from the 
                    // client. Display it on the console.
                    Console.WriteLine("Read {0} bytes from socket. \n Data : {1}",
                        content.Length, content );
                    // Echo the data back to the client.
                    Send(handler, content);
                } else {
                    // Not all data received. Get more.
                    handler.BeginReceive(state.buffer, 0, StateObject.BufferSize, 0,
                    new AsyncCallback(ReadCallback), state);
                }
            }
        }
    
        private static void Send(Socket handler, String data) {
            // Convert the string data to byte data using ASCII encoding.
            byte[] byteData = Encoding.ASCII.GetBytes(data);
    
            // Begin sending the data to the remote device.
            handler.BeginSend(byteData, 0, byteData.Length, 0,
                new AsyncCallback(SendCallback), handler);
        }
    
        private static void SendCallback(IAsyncResult ar) {
            try {
                // Retrieve the socket from the state object.
                Socket handler = (Socket) ar.AsyncState;
    
                // Complete sending the data to the remote device.
                int bytesSent = handler.EndSend(ar);
                Console.WriteLine("Sent {0} bytes to client.", bytesSent);
    
                handler.Shutdown(SocketShutdown.Both);
            } catch (Exception e) {
                Console.WriteLine(e.ToString());
            }
        }
    
    
        public static int Main(String[] args) {
            StartListening();
            return 0;
        }
#endif
    public void Dispose()
    {
    }
}

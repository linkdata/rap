using System;
using System.Net.Sockets;

namespace Starcounter.Rap
{
    public class Conn
    {
        private Server _server;
        private Socket _socket;
        private ByteBuffer _rdbuf = new ByteBuffer(0x10000);
        private ByteBuffer _towrite = new ByteBuffer(0x10000);
        private ByteBuffer _writing = new ByteBuffer(0x10000);
        private SocketAsyncEventArgs _rdargs = new SocketAsyncEventArgs();
        private SocketAsyncEventArgs _wrargs = new SocketAsyncEventArgs();

        public Conn(Server server, Socket socket)
        {
            _server = server;
            _socket = socket;
            _rdargs.Completed += new EventHandler<SocketAsyncEventArgs>(ReadCompleted);
            _wrargs.Completed += new EventHandler<SocketAsyncEventArgs>(WriteCompleted);
            ReadSome();
        }
        
        public void Write(byte[] srcbuf)
        {
            _towrite.Write(srcbuf);
            WriteSome();
        }

        public void Write(byte[] srcbuf, int srcpos, int srclen)
        {
            _towrite.Write(srcbuf, srcpos, srclen);
            WriteSome();
        }

        private void WriteSome()
        {
            if (!_writing.IsEmpty || _towrite.IsEmpty)
                return;
            var temp = _towrite;
            _towrite = _writing;
            _writing = temp;
            _wrargs.SetBuffer(_writing.Buffer, 0, _writing.Length);
            if (!_socket.SendAsync(_wrargs))
                ProcessSend(_wrargs);
        }

        private void ReadSome()
        {
            if (_rdbuf.IsFull)
                _rdbuf.Expand();
            _rdargs.SetBuffer(_rdbuf.Buffer, _rdbuf.Length, _rdbuf.Unused);
            if (!_socket.ReceiveAsync(_rdargs))
                ProcessReceive(_rdargs);
        }

        private void ReadCompleted(object sender, SocketAsyncEventArgs e)
        {
            if (e.LastOperation != SocketAsyncOperation.Receive)
                throw new ArgumentException("The last operation completed on the socket was not a receive");
            ProcessReceive(e);
        }

        private void WriteCompleted(object sender, SocketAsyncEventArgs e)
        {
            if (e.LastOperation != SocketAsyncOperation.Send)
                throw new ArgumentException("The last operation completed on the socket was not a send");
            ProcessSend(e);
        }

        private void ProcessReceive(SocketAsyncEventArgs e)
        {
            if (e.SocketError != SocketError.Success)
            {
                Console.WriteLine("ProcessReceive Error {0}", e.SocketError);
                CloseClientSocket();
                return;
            }
            
            if (e.BytesTransferred > 0)
            {
                // Console.WriteLine("ProcessReceive {0}", e.BytesTransferred);
                _server.StatReadBytesAdd(e.BytesTransferred);
                _rdbuf.Produce(e.BytesTransferred);
                int pos = 0;
                while (pos + Frame.HeaderSize <= _rdbuf.Length)
                {
                    var neededBytes = Frame.NeededBytes(_rdbuf.Buffer, pos); 
                    if (pos + neededBytes > _rdbuf.Length)
                        break;
                    ProcessFrame(_rdbuf.Buffer, pos);
                    pos += neededBytes;
                }
                _rdbuf.Consume(pos);
            }
            
            ReadSome();
        }
        
        private void ProcessFrame(byte[] buffer, int offset)
        {
            var frame = new Frame(buffer, offset);
            if (frame.IsFinal)
            {
                // Header
                _towrite.Write((UInt16)8);
                _towrite.Write((UInt16)(frame.ExchangeId | 0x8000 | 0x4000));
                // Record type
                _towrite.Write(Frame.HeadTypeHTTPResponse);
                // Code
                _towrite.Write((UInt16)204);
                // Headers
                _towrite.Write(0);
                _towrite.Write(0);
                // Status text
                _towrite.Write(0);
                _towrite.Write(0);
                // Content Length
                _towrite.Write(0);
                WriteSome();
            }
            else
            {
                // send ack
                _towrite.Write((UInt16)0);
                _towrite.Write((UInt16)(frame.ExchangeId));
                WriteSome();
            }
        }

        private void ProcessSend(SocketAsyncEventArgs e)
        {
            if (e.SocketError == SocketError.Success)
            {
                // Console.WriteLine("ProcessSend {0}", e.BytesTransferred);
                _server.StatWriteBytesAdd(e.BytesTransferred);
                _writing.Consume(e.BytesTransferred);
                WriteSome();
            }
            else
            {
                Console.WriteLine("ProcessSend Error {0}", e.SocketError);
                CloseClientSocket();
            }
        }

        private void CloseClientSocket()
        {
            try
            {
                _socket.Shutdown(SocketShutdown.Send);
            }
            catch (Exception) { }
            // _socket.Disconnect(false);
            _socket = null;
            _server.StatConnCountDec();
        }
    }
}

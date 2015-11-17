using System;

namespace Starcounter.Rap
{
    internal sealed class ByteBuffer
    {
        private int _length = 0;
        private int _maxcap; 
        private byte[] _buffer;

        public ByteBuffer(int capacity = 256, int maxcapacity = 10 * 1024 * 1024)
        {
            if (maxcapacity < 1 || maxcapacity > 1024 * 1024 * 1024)
                throw new ArgumentOutOfRangeException("invalid maxcapacity");
            if (capacity < 1 || capacity > maxcapacity)
                throw new ArgumentOutOfRangeException("invalid capacity");
            _maxcap = maxcapacity;
            _buffer = new byte[capacity];
        }
        
        public byte[] Buffer
        {
            get { return _buffer; }
        }
        
        public int Length
        {
            get { return _length; }
        }
        
        public int Capacity
        {
            get { return _buffer.Length; }
        }
        
        public int MaxCapacity
        {
            get { return _maxcap; }
        }
        
        public int Unused
        {
            get { return Capacity - Length; }
        }

        public bool IsEmpty
        {
            get { return _length == 0; }
        }
        
        public bool IsFull
        {
            get { return Length >= Capacity; }
        }
        
        public void Expand()
        {
            Reserve(Capacity + 1);
        }
        
        public void Reserve(int size)
        {
            if (Capacity >= size)
                return;
            if (size < 1 || size > _maxcap)
                throw new ArgumentOutOfRangeException();
            size--;
            size |= size >> 1;
            size |= size >> 2;
            size |= size >> 4;
            size |= size >> 8;
            size |= size >> 16;
            size++;
            var newbuf = new byte[size];
            if (_buffer != null)
                _buffer.CopyTo(newbuf, 0);
            _buffer = newbuf;
        }
        
        public void Write(byte b)
        {
            Reserve(Length + 1);
            _buffer[_length] = b;
            _length++;
        }
        
        public void Write(UInt16 n)
        {
            Reserve(Length + 2);
            _buffer[_length] = (byte)(n >> 8);
            _buffer[_length + 1] = (byte)(n);
            _length += 2;
        }

        public void Write(byte[] srcbuf)
        {
            Reserve(_length + srcbuf.Length);
            srcbuf.CopyTo(_buffer, _length);
            _length += srcbuf.Length;
        }
        
        public void Write(byte[] srcbuf, int srcpos, int srclen)
        {
            Reserve(_length + srclen);
            Array.Copy(srcbuf, srcpos, _buffer, _length, srclen);
            _length += srclen;
        }
        
        public void Produce(int num)
        {
            if (num < 0 || _length + num > Capacity)
                throw new ArgumentOutOfRangeException();
            _length += num;
        }
        
        public void Clear()
        {
            _length = 0;
        }

        public void Consume(int num)
        {
            if (num == _length)
            {
                _length = 0;
            }
            else if (num < 1)
            {
                if (num < 0)
                    throw new ArgumentOutOfRangeException();
            }
            else if (num > _length)
            {
                throw new ArgumentOutOfRangeException();
            }
            else
            {
                Array.Copy(_buffer, num, _buffer, 0, _length - num);
                _length -= num;
            }
        }
    }
}

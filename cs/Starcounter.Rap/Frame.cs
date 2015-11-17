using System;

namespace Starcounter.Rap
{
	public struct Frame
	{
		public const int HeaderSize = 4;
		public const UInt16 ConnExchangeId = 0x1fff;
		public const UInt16 MaxExchangeId = ConnExchangeId - 1;
		public const byte HeadTypeHTTPRequest = 0x01;
		public const byte HeadTypeHTTPResponse = 0x02;
		
		// these masks apply to _buffer[_offset + 2]
		private const byte _mask_final = 0x80;
		private const byte _mask_head  = 0x40;
		private const byte _mask_body  = 0x20;
		private const byte _mask_id    = ConnExchangeId >> 8;
		
		private readonly byte[] _buffer;
		private readonly int _offset;
		
		static public int NeededBytes(byte[] buffer, int offset)
		{
			if ((buffer[offset + 2] & (_mask_head | _mask_body)) == 0)
				return HeaderSize;
			return HeaderSize + ((int)(buffer[offset]) << 8) + (int)(buffer[offset + 1]);
		}
		
		public Frame(byte[] buffer, int offset)
		{
			_buffer = buffer;
			_offset = offset;
		}
		
		public byte GetByte(int pos)
		{
			return _buffer[_offset + pos];
		}
		
		public void SetByte(int pos, byte x)
		{
			_buffer[_offset + pos] = x;
		}
		
		public bool HasPayload
		{
			get { return (GetByte(2) & (_mask_head | _mask_body)) != 0; }
		}
		
		public int PayloadSize
		{
			get { return HasPayload ? SizeValue : 0; }
		}
		
		public UInt16 ExchangeId
		{
			get { return (UInt16)( (int)(GetByte(2) & _mask_id) << 8 | GetByte(3) ); }
		}
		
		public int SizeValue
		{
			get { return ((int)GetByte(0)) << 8 | (int)GetByte(1); }
			set
			{
				SetByte(0, (byte)(value >> 8));
				SetByte(1, (byte)(value));
			}
		}
		
		public bool HasHead
		{
			get { return (GetByte(2) & (_mask_head)) != 0; }
		}

		public bool HasBody
		{
			get { return (GetByte(2) & (_mask_body)) != 0; }
		}
		
		public bool IsFinal
		{
			get { return (GetByte(2) & (_mask_final)) != 0; }
		}
	}
}

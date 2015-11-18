using System;

namespace Starcounter.Rap
{
    public class Exchange
    {
		private readonly Conn _conn;
		private readonly UInt16 _id;
		
		public Exchange(Conn conn, UInt16 id)
		{
			_conn = conn;
			_id = id;
		}
	}
}

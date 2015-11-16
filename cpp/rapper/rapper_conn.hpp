#ifndef RAPPER_CONN_HPP
#define RAPPER_CONN_HPP

#include "rapper.hpp"
#include "rap_conn.hpp"

#include <boost/asio.hpp>
#include <boost/bind.hpp>
#include <boost/shared_ptr.hpp>
#include <boost/enable_shared_from_this.hpp>

namespace rapper {

template<typename exchange_t>
class conn
    : public boost::enable_shared_from_this< conn<exchange_t> >
    , public config<exchange_t>::conn_base
{
public:
  typedef typename config<exchange_t>::conn_base conn_base;
  typedef typename config<exchange_t>::conn_type conn_type;
  typedef typename config<exchange_t>::server_type server_type;
  typedef typename config<exchange_t>::server_ptr server_ptr;

  conn(server_ptr server)
    : conn_base(*static_cast<server_type*>(server.get()))
    , socket_(server->io_service())
  {}

  boost::asio::ip::tcp::socket& socket() {
    return socket_;
  }

  void start() {
    this->read_some();
    return;
  }

  void read_stream(char* buf_ptr, size_t buf_max) {
    // fprintf(stderr, "rapper::conn::read_stream(%p, %lu)\n", buf_ptr, buf_max);
    socket_.async_read_some(
          boost::asio::buffer(buf_ptr, buf_max),
          boost::bind(
            &conn_type::handle_read,
            this->shared_from_this(),
            boost::asio::placeholders::error,
            boost::asio::placeholders::bytes_transferred
            )
          );
    return;
  }

  void write_stream(const char* src_ptr, size_t src_len) {
    // fprintf(stderr, "rapper::conn::write_stream(%p, %lu)\n", src_ptr, src_len);
    boost::asio::async_write(
          socket_,
          boost::asio::buffer(src_ptr, src_len),
          boost::bind(
            &conn_type::handle_write,
            this->shared_from_this(),
            boost::asio::placeholders::error,
            boost::asio::placeholders::bytes_transferred
            )
          );
    return;
  }

private:
  boost::asio::ip::tcp::socket socket_;

  void handle_read(const boost::system::error_code& error, size_t bytes_transferred)
  {
    if (error) {
      fprintf(stderr, "rapper::conn::handle_read(%s, %lu)\n", error.message().c_str(), bytes_transferred);
      return;
    }
    this->read_stream_ok(bytes_transferred);
    this->read_some();
    return;
  }

  void handle_write(const boost::system::error_code& error, size_t bytes_transferred)
  {
    if (error) {
      fprintf(stderr, "rapper::conn::handle_write(%s, %lu)\n", error.message().c_str(), bytes_transferred);
      return;
    }
    this->write_stream_ok(bytes_transferred);
    this->write_some();
    return;
  }
};

} // namespace rapper

#endif // RAPPER_CONN_HPP


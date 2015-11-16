#ifndef RAPPER_SERVER_HPP
#define RAPPER_SERVER_HPP

#include "rapper.hpp"
#include "rapper_conn.hpp"
#include "rap_server.hpp"

#include <boost/bind.hpp>
#include <boost/asio.hpp>
#include <boost/shared_ptr.hpp>
#include <boost/enable_shared_from_this.hpp>

namespace rapper {

template <typename exchange_t>
class server
    : public boost::enable_shared_from_this< server<exchange_t> >
    , public config<exchange_t>::server_base
{
public:
  typedef typename config<exchange_t>::server_type server_type;
  typedef typename config<exchange_t>::server_ptr server_ptr;
  typedef typename config<exchange_t>::conn_type conn_type;
  typedef typename config<exchange_t>::conn_ptr conn_ptr;

  virtual ~server() {}

  server(boost::asio::io_service& io_service, short port)
    : io_service_(io_service)
    , acceptor_(io_service, boost::asio::ip::tcp::endpoint(boost::asio::ip::tcp::v4(), port))
    , timer_(io_service)
    , last_head_count_(0)
    , last_read_bytes_(0)
    , last_write_bytes_(0)
    , stat_rps_(0)
    , stat_mbps_in_(0)
    , stat_mbps_out_(0)
  {}

  void start() {
    timer_.expires_from_now(boost::posix_time::seconds(1));
    timer_.async_wait(
          boost::bind(
            &server_type::handle_timeout,
            this->shared_from_this(),
            boost::asio::placeholders::error
            )
          );
    start_accept();
  }

  virtual void once_per_second() {}

  const boost::asio::io_service& io_service() const { return io_service_; }
  boost::asio::io_service& io_service() { return io_service_; }

protected:
  uint64_t last_head_count_;
  uint64_t last_read_iops_;
  uint64_t last_read_bytes_;
  uint64_t last_write_iops_;
  uint64_t last_write_bytes_;
  uint64_t stat_rps_;
  uint64_t stat_iops_in_;
  uint64_t stat_mbps_in_;
  uint64_t stat_iops_out_;
  uint64_t stat_mbps_out_;

private:
  boost::asio::io_service& io_service_;
  boost::asio::ip::tcp::acceptor acceptor_;
  boost::asio::deadline_timer timer_;

  void start_accept()
  {
    conn_ptr conn(new conn_type(this->shared_from_this()));
    acceptor_.async_accept(
          conn->socket(),
          boost::bind(
            &server_type::handle_accept,
            this->shared_from_this(),
            conn,
            boost::asio::placeholders::error
            )
          );
    return;
  }

  void handle_accept(
      conn_ptr new_conn,
      const boost::system::error_code& error
      )
  {
    if (!error)
    {
      new_conn->start();
    }

    start_accept();
    return;
  }

  void handle_timeout(const boost::system::error_code& e)
  {
    if (e != boost::asio::error::operation_aborted)
    {
      uint64_t n;

      n = this->stat_head_count();
      stat_rps_ = n - last_head_count_;
      last_head_count_ = n;

      n = this->stat_read_iops();
      stat_iops_in_ = n - last_read_iops_;
      last_read_iops_ = n;

      n = this->stat_read_bytes();
      stat_mbps_in_ = ((n - last_read_bytes_) * 8) / 1024 / 1024;
      last_read_bytes_ = n;

      n = this->stat_write_iops();
      stat_iops_out_ = n - last_write_iops_;
      last_write_iops_ = n;

      n = this->stat_write_bytes();
      stat_mbps_out_ = ((n - last_write_bytes_) * 8) / 1024 / 1024;
      last_write_bytes_ = n;

      once_per_second();
    }
    timer_.expires_from_now(boost::posix_time::seconds(1));
    timer_.async_wait(
          boost::bind(
            &server_type::handle_timeout,
            this->shared_from_this(),
            boost::asio::placeholders::error
            )
          );
  }
};

} // namespace rapper

#endif // RAPPER_SERVER_HPP


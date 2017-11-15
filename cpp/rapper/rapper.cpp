/**
 * @brief REST Aggregation Protocol test server
 * @author Johan Lindh <johan@linkdata.se>
 * @note Copyright (c)2015-2017 Johan Lindh
 */

#include "rap_exchange.hpp"
#include "rap_reader.hpp"
#include "rap_request.hpp"
#include "rap_response.hpp"
#include "rap_writer.hpp"
#include "rapper_conn.hpp"
#include "rapper_server.hpp"

#include <boost/asio.hpp>

#include <iostream>
#include <sstream>

class rap_exchange;
typedef rapper::config<rap_exchange> rapper_cfg;

class rap_exchange : public rapper_cfg::exchange_base {
 public:
  explicit rap_exchange(rapper_cfg::conn_type &conn)
      : rapper_cfg::exchange_base(conn) {}

  rap::error process_head(rap::reader &r) {
    if (r.read_tag() != rap::record::tag_http_request)
      return rap::rap_err_unknown_frame_type;
    rap::request req(r);
    req_echo_.clear();
    req.render(req_echo_);
    req_echo_ += '\n';
    header().set_head();
    int64_t content_length = req.content_length();
    if (content_length >= 0) content_length += req_echo_.size();
    rap::writer(*this) << rap::response(200, content_length);
    header().set_body();
    sputn(req_echo_.data(), req_echo_.size());
    return r.error();
  }

  rap::error process_body(rap::reader &r) {
    assert(r.size() > 0);
    header().set_body();
    sputn(r.data(), r.size());
    r.consume();
    return r.error();
  }

  rap::error process_final(rap::reader &r) {
    assert(r.size() == 0);
    header().set_final();
    pubsync();
    return r.error();
  }

 private:
  rap::string_t req_echo_;
};

class rap_server : public rapper_cfg::server_type {
 public:
  typedef boost::shared_ptr<rap_server> ptr;
  virtual ~rap_server() {}

  rap_server(boost::asio::io_service &io_service, short port)
      : rapper_cfg::server_type(io_service, port) {}

  virtual void once_per_second() {
    if (stat_rps_ || stat_mbps_in_ || stat_mbps_out_) {
      fprintf(
          stderr,
          "%llu Rps - IN: %llu Mbps, %llu iops - OUT: %llu Mbps, %llu iops\n",
          stat_rps_, stat_mbps_in_, stat_iops_in_, stat_mbps_out_,
          stat_iops_out_);
    }
  }
};

int main(int, char *[]) {
  assert(sizeof(rap::text) == sizeof(const char *) + sizeof(size_t));
  assert(sizeof(rap::header) == 4);
  try {
    boost::asio::io_service io_service;
    rap_server::ptr s(new rap_server(io_service, 10111));
    s->start();
    io_service.run();
  } catch (std::exception &e) {
    std::cerr << "Exception: " << e.what() << "\n";
  }

  return 0;
}

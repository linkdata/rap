#ifndef RAP_CONN_HPP
#define RAP_CONN_HPP

#include "rap.hpp"
#include "rap_frame.hpp"
#include "rap_text.hpp"

#include <cassert>
#include <cstdint>
#include <vector>

namespace rap {

template <typename server_t, typename conn_t, typename exchange_t>
class conn {
public:
  static const int16_t max_send_window = 8;

  explicit conn(server_t& server)
    : next_id_(0)
    , server_(server)
    , buf_ptr_(buffer_)
    , buf_end_(buf_ptr_)
    , exchanges_(rap_max_exchange_id+1, exchange_t(*static_cast<conn_t*>(this)))
  {
    // assert correctly initialized exchange vector
    assert(exchanges_.size() == rap_max_exchange_id+1);
    for (uint16_t id = 0; id < exchanges_.size(); ++id) {
      assert(&(exchanges_[id].conn()) == this);
      assert(exchanges_[id].id() == id);
      assert(exchanges_[id].send_window() == send_window());
    }
  }

  void process_conn(const frame* f) {
    assert(!"TODO!");
  }

  // results in a call to conn_t::read_stream()
  void read_some() {
    self().read_stream(buf_end_, sizeof(buffer_) - (buf_end_ - buffer_));
    return;
  }

  // new stream data available in the read buffer
  void read_stream_ok(size_t bytes_transferred)
  {
    server().stat_read_bytes_add(bytes_transferred);
    buf_end_ += bytes_transferred;
    assert(buf_end_ <= buffer_ + sizeof(buffer_));

    const char* src_ptr = buffer_;
    while (src_ptr + rap_frame_header_size <= buf_end_) {
      size_t src_len = frame::needed_bytes(src_ptr);
      if (src_ptr + src_len > buf_end_)
        break;
      const frame* f = reinterpret_cast<const frame*>(src_ptr);
      uint16_t id = f->header().id();
      if (id == rap_conn_exchange_id) {
        process_conn(f);
      } else if (id < exchanges_.size()) {
        exchanges_[f->header().id()].process_frame(f);
      } else {
        // exchange id out of range
        fprintf(stderr, "rap::conn::read_stream_ok(): exchange id %04x out of range\n", id);
        return;
      }
      src_ptr += src_len;
      assert(src_ptr <= buf_end_);
    }

    // move trailing unprocessed input to the start
    if (src_ptr > buffer_) {
      char* dst_ptr = buffer_;
      while (src_ptr < buf_end_)
        *(dst_ptr++) = *src_ptr++;
      buf_end_ = dst_ptr;
    }
  }

  // buffered write to the network stream
  error write(const char* src_ptr, size_t src_len) {
    buf_towrite_.insert(buf_towrite_.end(), src_ptr, src_ptr + src_len);
    write_some();
    return rap_err_ok;
  }

  // writes any buffered data to the stream using conn_t::write_stream()
  void write_some() {
    if (!buf_writing_.empty() || buf_towrite_.empty())
      return;
    buf_writing_.swap(buf_towrite_);
    self().write_stream(buf_writing_.data(), buf_writing_.size());
    return;
  }

  void write_stream_ok(size_t bytes_transferred) {
    assert(bytes_transferred == buf_writing_.size());
    server().stat_write_bytes_add(bytes_transferred);
    buf_writing_.clear();
    return;
  }

  // used while constructing when initializing vector of exchanges
  uint16_t next_id() {
    if (next_id_ > rap_max_exchange_id)
      return rap_conn_exchange_id;
    return next_id_++;
  }

  const conn_t& self() const { return *static_cast<const conn_t*>(this); }
  conn_t& self() { return *static_cast<conn_t*>(this); }
  server_t& server() const { return server_; }
  const exchange_t& exchange(uint16_t id) const { return exchanges_[id]; }
  exchange_t& exchange(uint16_t id) { return exchanges_[id]; }
  int16_t send_window() const { return max_send_window; }

private:
  server_t& server_;
  uint16_t next_id_;
  char buffer_[rap_frame_max_size*2];
  char* buf_ptr_;                   //< current position within buffer
  char* buf_end_;                   //< expected end of frame or NULL if no header yet
  std::vector<char> buf_towrite_;
  std::vector<char> buf_writing_;
  std::vector<exchange_t> exchanges_;

  // must be implemented in conn_t
  void read_stream(char* buf_ptr, size_t buf_max) {
    // read input stream into buffer provided,
    // call read_stream_ok() when done (or abort the conn)
    assert(0);
    return;
  }

  // must be implemented in conn_t
  void write_stream(const char* src_ptr, size_t src_len) {
    // write provided buffer to the output stream
    // call write_stream_ok() when done (or abort the conn)
    assert(0);
    return;
  }

};

} // namespace rap

#endif // RAP_CONN_HPP

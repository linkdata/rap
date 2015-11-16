#ifndef RAP_TEXT_HPP
#define RAP_TEXT_HPP

#include "rap.hpp"

// defined in rap_textmap.c, generated from rap_textmap.gperf using GNU gperf.
extern "C" {
  unsigned int rap_textmap_max_key();
  const char* rap_textmap_from_key(register unsigned int key, register size_t* p_len);
  unsigned int rap_textmap_to_key(register const char* str, register size_t len);
}

namespace rap {

class text {
public:
  text()
    : data_(NULL)
    , size_(0)
  {}

  text(const text& other)
    : data_(other.data_)
    , size_(other.size_)
  {}

  explicit text(const char* ptr, size_t len)
    : data_(ptr)
    , size_(len)
  {}

  explicit text(unsigned char map_index) {
    data_ = rap_textmap_from_key(static_cast<unsigned>(map_index), &size_);
  }

  text& operator=(const text& other) {
    data_ = other.data_;
    size_ = other.size_;
    return *this;
  }

  bool operator==(const char* c_str) const {
    if (!c_str)
      return is_null();
    size_t len = strlen(c_str);
    if (len != size())
      return false;
    return !memcmp(data(), c_str, len);
  }

  bool operator!=(const char* c_str) const {
    return !operator==(c_str);
  }

  void render(string_t& out) const {
    if (!empty())
      out.append(data(), size());
  }

  string_t str() const {
    string_t retv;
    render(retv);
    return retv;
  }

  bool is_null() const { return data_ == NULL; }
  bool empty() const { return !size_; }
  const char* data() const { return data_; }
  size_t size() const { return size_; }

private:
  const char* data_;
  size_t size_;
};

} // namespace rap

#endif // RAP_TEXT_HPP


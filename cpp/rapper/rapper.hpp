#ifndef RAPPER_HPP
#define RAPPER_HPP

#include "rap.hpp"

#include <boost/shared_ptr.hpp>

namespace rapper {

template <typename exchange_t>
class server;
template <typename exchange_t>
class conn;

template <typename exchange_t>
struct config {
  typedef rapper::server<exchange_t> server_type;
  typedef rapper::conn<exchange_t> conn_type;
  typedef boost::shared_ptr<server_type> server_ptr;
  typedef boost::shared_ptr<conn_type> conn_ptr;
  typedef rap::server<server_type, conn_type, exchange_t> server_base;
  typedef rap::conn<server_type, conn_type, exchange_t> conn_base;
  typedef rap::exchange<server_type, conn_type, exchange_t> exchange_base;
};

}  // namespace rapper

#endif  // RAPPER_HPP
